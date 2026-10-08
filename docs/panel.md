# Panel

## What the panel is

The panel is a web page. Staff and tenants use it to work with Bedrock.
The panel runs at `https://console.<platform.host>`.
The address `https://<platform.host>` sends the user to the panel.
The panel is a React single-page app. The proxy serves it from the Bedrock image.
The panel speaks only to its own origin. It uses relative URLs.

## Sign in and sign out

The panel starts the sign-in at `/oauth/login`. The user then comes back to the same page.
The session lasts until the end time that the session answer shows.
When the session ends, the panel sends the user to sign in again. It does this one time.
When the sign-in fails two times in 10 seconds, the panel stops. It shows an error card with a retry link.
The user menu has the sign-out item. It sends `POST /oauth/logout` and then opens `/`.

## The two perspectives

The admin perspective shows when the session answer says `admin`. The group `bedrock-admins` gives this.
The project perspective lists the projects of the user. The panel reads them from `GET /proxy/v1/projects`.
A user who is not an admin cannot open the admin routes. The panel sends this user to `/p`.
The server stays the authority. The panel only hides what the user cannot use.

## Pages

| Route | Page |
|---|---|
| `/` | Redirect to the stored perspective |
| `/admin` | Redirect to `/admin/hosts` |
| `/admin/hosts` | Hosts |
| `/admin/hosts/:name` | Host detail: overview, inventory, metrics, maintenance mode |
| `/admin/settings` | Settings, grouped by prefix |
| `/admin/upgrades` | Upgrade phase, releases, resume and abort |
| `/admin/tenants` | Tenants |
| `/admin/tenants/:name` | Tenant detail: quota use and projects |
| `/admin/cluster-images` | Catalog images |
| `/admin/cluster-images/:name` | Catalog image detail |
| `/admin/addons` | Addons |
| `/p` | Redirect to the stored project, else the first project |
| `/p/:project` | Redirect to the virtual machines |
| `/p/:project/vms` | Virtual machines |
| `/p/:project/vms/new` | Create a virtual machine from an image |
| `/p/:project/vms/new-iso` | Install a virtual machine from an ISO |
| `/p/:project/vms/:name` | Virtual machine detail: `?tab=overview`, `console`, `metrics`, `disks`, `networks` |
| `/p/:project/volumes` | Volumes |
| `/p/:project/volumes/:name` | Volume detail with usage chart |
| `/p/:project/snapshots` | Volume snapshots |
| `/p/:project/images` | Project images and the catalog |
| `/p/:project/images/:name` | Image detail |
| `/p/:project/backups` | Backups, restores and the backup history |
| `/p/:project/ssh-keys` | SSH keys |
| `/p/:project/networks` | Networks of the tenant |
| `/p/:project/subnets` | Subnets |
| `/p/:project/elastic-ips` | Elastic IPs, attach and detach |
| `/p/:project/load-balancers` | Load balancers |
| `/p/:project/dns-zones` | DNS zones |
| `/p/:project/security-groups` | Security groups |
| `/p/:project/buckets` | Buckets |
| `/p/:project/object-users` | Object storage users and their credentials |
| `/account` | Language, theme and downloads |

Every other kind with a namespace has a detail page at `/p/:project/<path>/:name`.

## Live updates

Every list and every detail page stays live. One WebSocket carries all updates.
The page loads a list first. It then subscribes at the version of that list.
When the proxy sends `relist`, the page loads the list again and subscribes again.
When the connection breaks, the panel connects again with a growing delay.
The indicator near the filter box shows `Live`, `Connecting` or `Paused`.
`Paused` means that the server refused the live stream for this page. The data on the page stays.

## Permissions

The panel shows an action only after the proxy says that the user can do it.
The panel asks with `POST /proxy/v1/authz`. One page sends one request.
A check that is pending or failed hides the action.
When the server refuses an action that the panel showed, the panel shows a message.
If a button is missing, ask a project owner or an admin for the role.

## Languages and direction

The panel speaks English, Persian and Arabic.
Persian and Arabic read from right to left. The layout mirrors.
IP addresses, CIDRs, names, YAML and JSON stay left to right.
Change the language in the top bar or on the account page. The panel keeps the choice in the browser.
A native speaker must review the Persian and Arabic text before a release.

## Themes

The panel has a light theme and a dark theme. The choice `System` follows the browser.
Change the theme in the top bar or on the account page.

## Consoles

The console tab has a graphical console and a serial console.
The graphical console uses noVNC. The serial console uses xterm.
The console opens only when the machine runs and the user can read `virtualmachines/console/vnc`.
Both consoles load when the user opens the tab.
When the session ends, the console sends the user to sign in again.

## Charts

Charts use the metrics endpoint of the proxy. The queries are:
`vm.cpu`, `vm.memory`, `vm.network.rx`, `vm.network.tx`, `vm.disk.read`, `vm.disk.write`,
`volume.used`, `volume.capacity`, `host.cpu`, `host.memory`, `host.network.rx`, `host.network.tx`,
`ceph.used` and `ceph.capacity`.
Charts need Prometheus. Without it, the page shows `Metrics are not available`.
The chart stays left to right. A hidden text gives the minimum, the maximum and the last value.

## Forms

Forms come from the API schema of the kind. The panel reads `/openapi/v3`.
A field that cannot change is read-only when the user edits an object.
The panel sends `PUT` with the version that it read. A conflict asks the user to reload.
The panel never sends `status`.
A refused form shows the server messages on the right fields.
Some fields use a list of objects. Subnets, load balancers and buckets offer a list of names.
The panel also checks cron schedules, SSH keys, IP addresses, rules and bucket policies before it sends.

## Upload an ISO

Open the images page. Create an image with an upload source. Choose Upload in the row menu.
The panel asks for a one-time token. It then sends the file to the upload address.
The token stays in memory. The panel never writes it in the page address or in storage.
The upload address must be in `connect-src` of the panel policy.

## Downloads

The user menu and the account page offer `/kubeconfig`. They offer `/ca.crt` when a private CA exists.

## Build and develop

Run `make ui` to build the panel and the login app.
Run `pnpm --filter panel test` to run the tests.
The build writes into `internal/proxy/ui/dist`. The proxy embeds that directory.
Before the first build, the directory holds only `.keep`. The proxy then shows a placeholder page.
`pnpm --filter panel build` checks the size. The first script must stay under 600 KiB. The whole output must stay under 8 MiB. The consoles and the charts must be separate chunks.

## Add a new kind

1. Add one entry to `src/resources/registry.ts`.
2. Add columns in `src/resources/columns.tsx`.
3. Add one fixture in `src/fixtures/<plural>.json`.
4. Add the navigation entry in `src/shell/navigation.ts`.
The registry test reads the real CRDs. It fails when a plural or a scope is wrong.

## Add a language

1. Add the locale to `src/i18n.ts` and to `lingui.config.ts`.
2. Add `src/locales/<locale>/messages.po` with every id.
3. Run `pnpm --filter panel test`. The catalog test checks that every id exists.

## Known limits

The panel does not list VM snapshots as objects. The snapshot action shows the server message.
The panel does not restore a backup to a new virtual machine name. The restore keeps the name of the backed up machine.
The panel has no browser test in CI. The tests use jsdom.
