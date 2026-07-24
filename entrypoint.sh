#!/bin/sh
# Start telnet console for GNS3 access (port 5000)
busybox telnetd -l /bin/sh -p 5000 &
sleep 0.5
# Start the PRP daemon
exec prpd
