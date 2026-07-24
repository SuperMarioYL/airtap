# Airtap demo tape.
# Flow mirrors assets/demo.cast: airtap init -> airtap connect ->
# airtap run "给 server.go 加 /health 接口" (tool-call stream) -> airtap audit.
# The audit step shows only the loopback model dial — zero cloud egress.
#
# Render with:  vhs docs/demo.tape      (writes airtap_demo.gif)

Output airtap_demo.gif

Set FontSize 14
Set Width 100
Set Height 30

# 1) init — writes config + provisions the mTLS client cert
Type "airtap init"
Enter
Sleep 2s

# 2) connect — dials 信创-gpu-01 over mTLS, opens an audited session
Type "airtap connect"
Enter
Sleep 2s

# 3) run — agent edits server.go, builds, vets; tool-call stream
Hide
Type "airtap run \"给 server.go 加 /health 接口\""
Enter
Sleep 14s
Show

# 4) audit — full trail, only dial is the loopback model endpoint
Type "airtap audit"
Enter
Sleep 4s

# tail
Hide
Sleep 1s
Show
