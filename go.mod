module github.com/zmap/dns

go 1.22.0

toolchain go1.24.0

replace github.com/miekg/dns => ./

require (
	golang.org/x/net v0.35.0
	golang.org/x/sync v0.11.0
	golang.org/x/sys v0.30.0
	golang.org/x/tools v0.30.0
)

require golang.org/x/mod v0.23.0 // indirect
