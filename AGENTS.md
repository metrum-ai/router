# Agent instructions

## Definition of done

Do not treat a change as done until the relevant end-to-end check has been run
locally on this machine.

- For request-path, streaming, identifier rewrite, or tool-call changes: start a
  localhost router from the working tree and prove a tool-using client
  continuation (for example a coding agent that creates a file, then continues
  with a tool result). Unit tests alone are not enough.
- For other surfaces: run the Make or Go tests that cover the changed path, plus
  any local smoke those docs already require.

Record the commands that actually ran and their outcomes before opening a pull
request or declaring the work finished.

## Extension boundary

User code never runs inside the router process. Custom routing logic must run as a standalone external policy service.


## Admin API compatibility

In v5.0.0, machine admin clients use `/admin/v1` on the admin listener. Legacy machine `/admin/*`
routes are removed from the serving port. The browser reports UI and `/admin/auth/*` session routes
remain on the serving port.
