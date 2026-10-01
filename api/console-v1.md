# Nephos console WebSocket v1

`GET /v1/workspaces/default/instances/{id}/console` upgrades to a WebSocket
only for an authenticated, running instance. Use the bearer token in the
`Authorization` header and offer the `nephos.console.v1` subprotocol. Nephos
rejects non-local `Host` values and every nonempty browser `Origin` in M1. Do
not put the token in a URL, subprotocol, or application frame.

The first message must arrive within five seconds and must be one text `start`
message. Interactive mode sends
`{"type":"start","command":["/bin/bash"],"tty":true,"rows":24,"cols":80}`.
One-shot command mode sends the exact argv, for example
`{"type":"start","command":["/bin/echo","hello"],"tty":false}`; it omits
`rows` and `cols`. Nephos does not insert a shell or interpolate arguments.
Empty argv, NUL arguments, unknown fields, invalid dimensions, and a second
`start` are protocol errors.

| Direction | WebSocket message | Meaning |
|---|---|---|
| Client → server | Binary: byte `0` then raw bytes | Write to exec stdin. |
| Client → server | Text: `{"type":"stdin_eof"}` | Close non-TTY stdin only; continue reading output and the final exit. Invalid in TTY mode. |
| Client → server | Text: `{"type":"resize","rows":40,"cols":120}` | Resize a TTY; invalid in command mode. |
| Server → client | Binary: byte `1` then raw bytes | stdout, or combined PTY output. |
| Server → client | Binary: byte `2` then raw bytes | stderr in non-TTY mode only. |
| Server → client | Text: `{"type":"exit","exit_code":7}` | Final result after both output streams drain and runtime wait returns a status in 0–255. Normal WebSocket close follows. |
| Server → client | Text: `{"type":"error","message":"..."}` | Protocol or runtime failure; never implies exit zero. |

The channel byte is part of each complete binary message. Control messages
are limited to 4 KiB; data messages to 64 KiB including that byte. Limits
apply to assembled WebSocket messages, not individual fragments. Pending
writes have a 10-second deadline, but a healthy interactive shell has no
arbitrary idle timeout. stdout and stderr each preserve byte order; there is
no total order between them. Clients must treat a lost connection, an error
message, malformed or duplicate final result, or closure without a valid
`exit` message as failure. A command's nonzero `exit_code` is its real exit
status, not a transport error.

The console is the explicitly labeled serial/exec exception to learner
traffic entering through `nx-edge`: it does not provide direct host access
to Podman or bypass network rules for packets sent inside the instance.
