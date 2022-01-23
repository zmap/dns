#!/bin/bash -xe

LATEST_MIEKG_TAG=$(git tag --merged master | sort -V | tail -n 1)
ZDNS_TAG="${LATEST_MIEKG_TAG}-zdns"
echo $ZDNS_TAG