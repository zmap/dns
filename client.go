package dns

// A client implementation.

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"
)

const (
	dnsTimeout     time.Duration = 2 * time.Second
	tcpIdleTimeout time.Duration = 8 * time.Second
)

// A Conn represents a connection to a DNS server.
type Conn struct {
	UDP            net.PacketConn // a net.PacketConn holding the connection
	TCP            net.Conn
	RemoteAddr     string            // server address that our client is connecting with
	UDPSize        uint16            // minimum receive buffer for UDP messages
	TsigSecret     map[string]string // secret(s) for Tsig map[<zonename>]<base64 secret>, zonename must be in canonical form (lowercase, fqdn, see RFC 4034 Section 6.2)
	tsigRequestMAC string
}

// A Client defines parameters for a DNS client.
type Client struct {
	Conn

	Net          string      // if "tcp" or "tcp-tls" (DNS over TLS) a TCP query will be initiated, otherwise an UDP one (default is "" for UDP)
	LocalAddr    net.IP      // preferred address to dial from local machine
	ExistingConn bool        // temporary field to test if connection exists
	UDPSize      uint16      // minimum receive buffer for UDP messages
	TLSConfig    *tls.Config // TLS connection configuration
	Dialer       *net.Dialer // a net.Dialer used to set local address, timeouts and more
	// Timeout is a cumulative timeout for dial, write and read, defaults to 0 (disabled) - overrides DialTimeout, ReadTimeout,
	// WriteTimeout when non-zero.
	Timeout        time.Duration
	DialTimeout    time.Duration     // net.DialTimeout, defaults to 2 seconds - overridden by Timeout when that value is non-zero
	ReadTimeout    time.Duration     // net.Conn.SetReadTimeout value for connections, defaults to 2 seconds - overridden by Timeout when that value is non-zero
	WriteTimeout   time.Duration     // net.Conn.SetWriteTimeout value for connections, defaults to 2 seconds - overridden by Timeout when that value is non-zero
	TsigSecret     map[string]string // secret(s) for Tsig map[<zonename>]<base64 secret>, zonename must be in canonical form (lowercase, fqdn, see RFC 4034 Section 6.2)
	SingleInflight bool              // if true suppress multiple outstanding queries for the same Qname, Qtype and Qclass
	group          singleflight
}

// Exchange performs a synchronous UDP query. It sends the message m to the address
// contained in a and waits for a reply. Exchange does not retry a failed query, nor
// will it fall back to TCP in case of truncation.
// See client.Exchange for more information on setting larger buffer sizes.
func Exchange(m *Msg, a string) (r *Msg, err error) {
	client := Client{Net: "udp"}
	r, _, err = client.Exchange(m, a)
	return r, err
}

func (c *Client) dialTimeout() time.Duration {
	if c.Timeout != 0 {
		return c.Timeout
	}
	if c.DialTimeout != 0 {
		return c.DialTimeout
	}
	return dnsTimeout
}

func (c *Client) readTimeout() time.Duration {
	if c.ReadTimeout != 0 {
		return c.ReadTimeout
	}
	return dnsTimeout
}

func (c *Client) writeTimeout() time.Duration {
	if c.WriteTimeout != 0 {
		return c.WriteTimeout
	}
	return dnsTimeout
}

// Dial connects to the address on the named network.
func (c *Client) Dial(address string) (conn *Conn, err error) {
	network := c.Net
	if network == "" {
		network = "udp"
	}
	// We shouldn't see clients ever changing the type of connection they are, so,
	// if there's an existing UDP connection, we can reuse it.
	// Simply set the remote address and return it to the user.
	if network == "udp" {
		if c.ExistingConn {
			c.Conn.RemoteAddr = address
			return &c.Conn, nil
		}
		if c.LocalAddr == nil {
			// get preferred IP address for local machine
			initConn, err := net.Dial(network, "8.8.8.8:53")
			if err != nil {
				return nil, err
			}
			c.LocalAddr = initConn.LocalAddr().(*net.UDPAddr).IP
		}
		conn = new(Conn)
		// dial from local address
		conn.UDP, err = net.ListenPacket(network, c.LocalAddr.String()+":0")
		if err != nil {
			log.Fatal("unable to create socket", err)
			return nil, err
		}
		conn.RemoteAddr = address
		c.ExistingConn = true
		c.Conn = *conn
		return conn, nil
	} else { // tcp
		useTLS := strings.HasPrefix(network, "tcp") && strings.HasSuffix(network, "-tls")
		conn = new(Conn)
		var d net.Dialer
		if c.Dialer == nil {
			d = net.Dialer{Timeout: c.getTimeoutForRequest(c.dialTimeout())}
		} else {
			d = *c.Dialer
		}
		if useTLS {
			network = strings.TrimSuffix(network, "-tls")
			conn.TCP, err = tls.DialWithDialer(&d, network, address, c.TLSConfig)
		} else {
			conn.TCP, err = d.Dial(network, address)
		}
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	panic("should never reach this state")
}

// Exchange performs a synchronous query. It sends the message m to the address
// contained in a and waits for a reply. Basic use pattern with a *dns.Client:
//
//	c := new(dns.Client)
//	in, rtt, err := c.Exchange(message, "127.0.0.1:53")
//
// Exchange does not retry a failed query, nor will it fall back to TCP in
// case of truncation.
// It is up to the caller to create a message that allows for larger responses to be
// returned. Specifically this means adding an EDNS0 OPT RR that will advertise a larger
// buffer, see SetEdns0. Messages without an OPT RR will fallback to the historic limit
// of 512 bytes
func (c *Client) Exchange(m *Msg, address string) (r *Msg, rtt time.Duration, err error) {
	if !c.SingleInflight {
		return c.exchange(m, address)
	}

	q := m.Question[0]
	key := fmt.Sprintf("%s:%d:%d", q.Name, q.Qtype, q.Qclass)
	r, rtt, err, shared := c.group.Do(key, func() (*Msg, time.Duration, error) {
		return c.exchange(m, address)
	})
	if r != nil && shared {
		r = r.Copy()
	}

	return r, rtt, err
}

func (c *Client) exchange(m *Msg, a string) (r *Msg, rtt time.Duration, err error) {
	var co *Conn

	co, err = c.Dial(a)

	if err != nil || co == nil {
		return nil, 0, err
	}

	opt := m.IsEdns0()
	// If EDNS0 is used use that for size.
	if opt != nil && opt.UDPSize() >= MinMsgSize {
		co.UDPSize = opt.UDPSize()
	}
	// Otherwise use the client's configured UDP size.
	if opt == nil && c.UDPSize >= MinMsgSize {
		co.UDPSize = c.UDPSize
	}

	co.TsigSecret = c.TsigSecret
	t := time.Now()
	// write with the appropriate write timeout
	co.SetWriteDeadline(t.Add(c.getTimeoutForRequest(c.writeTimeout())))
	if err = co.WriteMsg(m); err != nil {
		return nil, 0, err
	}

	co.SetReadDeadline(time.Now().Add(c.getTimeoutForRequest(c.readTimeout())))
	r, err = co.ReadMsg()
	if err == nil && r.Id != m.Id {
		err = ErrId
	}
	rtt = time.Since(t)
	return r, rtt, err
}

// ReadMsg reads a message from the connection co.
// If the received message contains a TSIG record the transaction signature
// is verified. This method always tries to return the message, however if an
// error is returned there are no guarantees that the returned message is a
// valid representation of the packet read.
func (co *Conn) ReadMsg() (*Msg, error) {
	p, err := co.ReadMsgHeader(nil)
	if err != nil {
		return nil, err
	}

	m := new(Msg)
	if err := m.Unpack(p); err != nil {
		// If an error was returned, we still want to allow the user to use
		// the message, but naively they can just check err if they don't want
		// to use an erroneous message
		return m, err
	}
	if t := m.IsTsig(); t != nil {
		if _, ok := co.TsigSecret[t.Hdr.Name]; !ok {
			return m, ErrSecret
		}
		// Need to work on the original message p, as that was used to calculate the tsig.
		err = TsigVerify(p, co.TsigSecret[t.Hdr.Name], co.tsigRequestMAC, false)
	}
	return m, err
}

// ReadMsgHeader reads a DNS message, parses and populates hdr (when hdr is not nil).
// Returns message as a byte slice to be parsed with Msg.Unpack later on.
// Note that error handling on the message body is not possible as only the header is parsed.

func (co *Conn) SetWriteDeadline(t time.Time) error {
	if co.TCP != nil {
		return co.TCP.SetWriteDeadline(t)
	} else {
		return co.UDP.SetWriteDeadline(t)
	}
}

func (co *Conn) SetReadDeadline(t time.Time) error {
	if co.TCP != nil {
		return co.TCP.SetReadDeadline(t)
	} else {
		return co.UDP.SetReadDeadline(t)
	}
}

func (co *Conn) Close() error {
	if co.TCP != nil {
		return co.TCP.Close()
	} else {
		return co.UDP.Close()
	}
}

func (co *Conn) ReadMsgHeader(hdr *Header) ([]byte, error) {
	var (
		p   []byte
		n   int
		err error
	)

	if co.TCP != nil {
		var length uint16
		if err := binary.Read(co.TCP, binary.BigEndian, &length); err != nil {
			return nil, err
		}

		p = make([]byte, length)
		n, err = io.ReadFull(co.TCP, p)
	} else {
		if co.UDPSize > MinMsgSize {
			p = make([]byte, co.UDPSize)
		} else {
			p = make([]byte, MinMsgSize)
		}
		n, err = co.Read(p)
	}

	if err != nil {
		return nil, err
	} else if n < headerSize {
		return nil, ErrShortRead
	}

	p = p[:n]
	if hdr != nil {
		dh, _, err := unpackMsgHdr(p, 0)
		if err != nil {
			return nil, err
		}
		*hdr = dh
	}
	return p, err
}

// Read implements the net.Conn read method.
func (co *Conn) Read(p []byte) (n int, err error) {
	if co.TCP != nil {
		var length uint16
		if err := binary.Read(co.TCP, binary.BigEndian, &length); err != nil {
			return 0, err
		}
		if int(length) > len(p) {
			return 0, io.ErrShortBuffer
		}

		return io.ReadFull(co.TCP, p[:length])
	} else {
		if co.UDP == nil {
			return 0, ErrConnEmpty
		}

		n, _, err = co.UDP.ReadFrom(p)
		return n, err
	}
	panic("should not reach")
}

// WriteMsg sends a message through the connection co.
// If the message m contains a TSIG record the transaction
// signature is calculated.
func (co *Conn) WriteMsg(m *Msg) (err error) {
	var out []byte
	if t := m.IsTsig(); t != nil {
		mac := ""
		if _, ok := co.TsigSecret[t.Hdr.Name]; !ok {
			return ErrSecret
		}
		out, mac, err = TsigGenerate(m, co.TsigSecret[t.Hdr.Name], co.tsigRequestMAC, false)
		// Set for the next read, although only used in zone transfers
		co.tsigRequestMAC = mac
	} else {
		out, err = m.Pack()
	}
	if err != nil {
		return err
	}
	_, err = co.Write(out)
	return err
}

// Write implements the net.Conn Write method.
func (co *Conn) Write(p []byte) (int, error) {
	if len(p) > MaxMsgSize {
		return 0, &Error{err: "message too large"}
	}
	if co.TCP != nil {
		l := make([]byte, 2)
		binary.BigEndian.PutUint16(l, uint16(len(p)))
		n, err := (&net.Buffers{l, p}).WriteTo(co.TCP)
		return int(n), err
	} else {
		dst, err := net.ResolveUDPAddr("udp", co.RemoteAddr)
		if err != nil {
			return 1, err
		}
		return co.UDP.WriteTo(p, dst)
	}
	panic("should not reach")
}

// Return the appropriate timeout for a specific request
func (c *Client) getTimeoutForRequest(timeout time.Duration) time.Duration {
	var requestTimeout time.Duration
	if c.Timeout != 0 {
		requestTimeout = c.Timeout
	} else {
		requestTimeout = timeout
	}

	return requestTimeout
}

// Dial connects to the address on the named network.
func Dial(network, address string) (conn *Conn, err error) {
	if network == "" || network == "udp" {
		conn = new(Conn)
		conn.UDP, err = net.ListenPacket(network, address)
		if err != nil {
			return nil, err
		}
		return conn, nil
	} else {
		conn = new(Conn)
		conn.TCP, err = net.Dial(network, address)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
}

// ExchangeContext performs a synchronous UDP query, like Exchange. It
// additionally obeys deadlines from the passed Context.
func ExchangeContext(ctx context.Context, m *Msg, a string) (r *Msg, err error) {
	client := Client{Net: "udp"}
	r, _, err = client.ExchangeContext(ctx, m, a)
	// ignorint rtt to leave the original ExchangeContext API unchanged, but
	// this function will go away
	return r, err
}

// ExchangeConn performs a synchronous query. It sends the message m via the connection
// c and waits for a reply. The connection c is not closed by ExchangeConn.
// Deprecated: This function is going away, but can easily be mimicked:
//
//	co := &dns.Conn{Conn: c} // c is your net.Conn
//	co.WriteMsg(m)
//	in, _  := co.ReadMsg()
//	co.Close()
//
func ExchangeConn(c net.Conn, m *Msg) (r *Msg, err error) {
	println("dns: ExchangeConn: this function is deprecated")
	co := new(Conn)
	co.UDP = c.(net.PacketConn)
	if err = co.WriteMsg(m); err != nil {
		return nil, err
	}
	r, err = co.ReadMsg()
	if err == nil && r.Id != m.Id {
		err = ErrId
	}
	return r, err
}

// DialTimeout acts like Dial but takes a timeout.
func DialTimeout(network, address string, timeout time.Duration) (conn *Conn, err error) {
	client := Client{Net: network, Timeout: timeout}
	return client.Dial(address)
}

// DialWithTLS connects to the address on the named network with TLS.
func DialWithTLS(network, address string, tlsConfig *tls.Config) (conn *Conn, err error) {
	if !strings.HasSuffix(network, "-tls") {
		network += "-tls"
	}
	client := Client{Net: network, TLSConfig: tlsConfig}
	return client.Dial(address)
}

// DialTimeoutWithTLS acts like DialWithTLS but takes a timeout.
func DialTimeoutWithTLS(network, address string, tlsConfig *tls.Config, timeout time.Duration) (conn *Conn, err error) {
	if !strings.HasSuffix(network, "-tls") {
		network += "-tls"
	}
	client := Client{Net: network, Timeout: timeout, TLSConfig: tlsConfig}
	return client.Dial(address)
}

// ExchangeContext acts like Exchange, but honors the deadline on the provided
// context, if present. If there is both a context deadline and a configured
// timeout on the client, the earliest of the two takes effect.
func (c *Client) ExchangeContext(ctx context.Context, m *Msg, a string) (r *Msg, rtt time.Duration, err error) {
	var timeout time.Duration
	if deadline, ok := ctx.Deadline(); !ok {
		timeout = 0
	} else {
		timeout = time.Until(deadline)
	}
	c.Timeout = timeout
	// not passing the context to the underlying calls, as the API does not support
	// context.
	return c.Exchange(m, a)
}
