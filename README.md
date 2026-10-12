# portal

The site at **https://42.bombatomica64.dev**: every 42 project I have done or am doing, with
status, mark and dates from the 42 API and a description, stack and repo link from
[`projects.yaml`](projects.yaml).

- `/` lists the projects; `/<project>/about` is a project's info page.
- `/<project>` is the project itself, running on lorenzo-dev at the port given in
  `projects.yaml`. Traefik routes it there; when the app is down, Traefik's `errors`
  middleware shows the info page instead.

## Configuration

| Env | |
|---|---|
| `FT_UID`, `FT_SECRET` | 42 API app (client_credentials). Optional: without them only the YAML shows. |
| `PROJECTS_URL` | Raw URL of `projects.yaml` on `main`, refreshed every 10 min. Unset → the copy built into the binary. |
| `APP_HOST` | Host the apps listen on (`100.107.70.55`), probed to show which apps are live. |
| `PORT` | Listen port, default `8080`. |

## Adding a project

1. Add it under `projects:` in `projects.yaml` with the next free port (4201, 4202, …).
2. If it has an app to serve, add `{name, port}` to `projects:` in
   `apps/portal42/values.yaml` in CodeDrevia/simple-server (PR), and run the app on
   lorenzo-dev bound to `100.107.70.55:<port>` with base path `/<project>`.

## Deploying code changes

Pushing to `main` builds `ghcr.io/alborghi-42/portal:<sha>`; set that tag in
`apps/portal42/values.yaml` in simple-server. Edits to `projects.yaml` alone need no deploy.

```bash
FT_UID=… FT_SECRET=… go run .   # http://localhost:8080
```
