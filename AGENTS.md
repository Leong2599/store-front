# You are building with Grit. Read this before writing any code.

Grit is a **full-stack meta-framework**: a **Go** backend (Gin + GORM) plus a
**React** frontend and a generated admin panel, wired together in one monorepo
and driven by a CLI. Think "Laravel/Rails developer experience, but the backend
is Go and the admin and clients are generated for you." Tagline: *Go + React.
Built with Grit.*

**The single most important rule:** Grit generates code — you do not hand-write
CRUD. When the user asks for a new entity (a Post, a Product, an Invoice), you run
`grit generate resource` and then customize the generated files. Hand-writing a
model + handler + service + schema + types + hooks + admin page by hand is the
wrong instinct here and fights the framework.

## This project's stack

Scaffold it with:

```bash
grit new store-front --triple --next
```

Clients in this project:

- **Go API** (Gin + GORM) — the source of truth. Every other client consumes it.
- **Web app** — Next.js (App Router), in `apps/web/` (or `frontend/` for a single-binary build).
- **Admin panel** — a generated, Filament-like dashboard in `apps/admin/` with data tables, forms, widgets, and a roles editor.

Start everything (Docker infra + all apps):

```bash
cd store-front
docker compose up -d          # PostgreSQL, Redis, MinIO, Mailhog
grit migrate                  # create tables + seed default roles
grit dev                      # run every app in the monorepo
```

Frontend: **Next.js (App Router)**.
- Routes live in `app/`. File naming: `page.tsx`, `layout.tsx`, `loading.tsx`, `error.tsx`.
- Server Components by default; add `'use client'` for interactivity. Navigation: `import { useRouter } from 'next/navigation'`, `import Link from 'next/link'`.

## The tech stack (do not substitute)

- **Backend:** Go 1.21+, **Gin** (web), **GORM** (ORM). PostgreSQL in prod, SQLite for tests.
- **Frontend:** Next.js (App Router) + **Tailwind CSS** + **shadcn/ui**. Data fetching is **TanStack Query (React Query)** — never raw `fetch` in components.
- **Validation:** **Zod**, shared between frontend and generated from the Go types.
- **Monorepo:** Turborepo + pnpm.
- **Infra:** Redis (cache + `asynq` jobs), S3-compatible storage (MinIO/R2/S3), Resend (email), Docker.

## Project layout

```
store-front/
├── grit.json                 # project manifest (architecture, frontend)
├── docker-compose.yml        # Postgres, Redis, MinIO, Mailhog
├── packages/shared/          # Zod schemas, TS types, constants (shared by all clients)
└── apps/
    ├── api/                  # Go backend
    │   ├── cmd/server/       # entry point
    │   └── internal/         # models, handlers, services, middleware, routes, ...
    ├── web/                  # Next.js (App Router)
    ├── admin/                # generated admin panel
    ├── expo/                 # mobile (if present)
    └── desktop/              # Wails desktop (if present)
```

(A `--single` build has no `apps/` — the Go code is at the root and the SPA lives in `frontend/`, embedded with `go:embed`.)

## Generating a resource — the core workflow

```bash
grit generate resource Post --fields "title:string,content:richtext,published:toggle,views:int"
```

From ONE command this generates, and wires up:

- **Go:** `internal/models/post.go` (GORM model), `internal/services/post_service.go`, `internal/handlers/post_handler.go`, and route registration under `/api/v1`.
- **Shared:** a Zod schema and TypeScript types in `packages/shared`.
- **Frontend:** React Query hooks (`use-posts.ts`) and, for triple/full, an admin resource definition + page.
- **Multi-client:** with mobile/desktop present, CRUD screens for those too.

It injects into existing files at **marker comments** — `// grit:models`, `// grit:handlers`, `// grit:routes:protected`, `// grit:seeders`, `// grit:sync`, and others. **Never delete these markers** — they are how generation and `grit remove resource` work.

### Field types

| Grit type | Go | Form control |
|-----------|-----|--------------|
| `string` | string | text input |
| `text` | string | textarea |
| `richtext` | string | rich text editor |
| `int` / `uint` / `float` | int / uint / float64 | number input |
| `bool` / `toggle` | bool | switch |
| `date` / `datetime` | time.Time | date picker |
| `slug` | string | auto-filled from a source field |
| `select` / `radio` | string | dropdown / radio group |
| `check` | string array | checkbox group |
| `string_array` | datatypes.JSONSlice[string] | tag input |
| `file` | *files.FileRef | single upload |
| `files` | files.FileRefs | multi upload |
| `belongs_to` | FK + relation | searchable select |
| `many_to_many` | join table | multi select |

**Modifiers** append after the type: `:unique`, `:required`, `:optional`, and `:auto` (optionally `:auto:PREFIX`) for a sequence-generated number.

```bash
email:string:unique          # unique index
notes:text:optional          # nullable
number:string:auto:INV       # INV-00001, minted in BeforeCreate, hidden from the form
```

**select / radio / check** take their choices in the third position, `value=Label` separated by `|`:

```bash
status:select:draft=Draft|sent=Sent|paid=Paid
```

### File, image and document uploads

`file` is one upload, `files` is many. The third position restricts what the picker and the server accept — one alias, or a bracketed list. Validation is enforced **server-side**, not just in the browser.

Accepted aliases: `image`, `video`, `audio`, `pdf`, `doc`, `excel`, `csv`, `zip`, `archive`, `all`.

```bash
# One hero image plus a gallery of images
grit generate resource Product --fields "name:string,price:float,hero:file:image,gallery:files:image"

# A single PDF
grit generate resource Contract --fields "title:string,signed:toggle,document:file:pdf"

# A zipped bundle plus mixed attachments
grit generate resource Release --fields "version:string:unique,bundle:file:zip,attachments:files:[pdf,doc,image]"

# A spreadsheet or CSV
grit generate resource Import --fields "label:string,source:file:[excel,csv]"

# Anything at all
grit generate resource Asset --fields "name:string,payload:file:all"
```

Uploads go to S3-compatible storage (MinIO locally, R2/S3/B2 in production) through presigned URLs, and images are processed on upload. A `file` field is `*files.FileRef` in Go — url, name, mime and size — so you get metadata rather than a bare string path. Never store raw bytes in the database.

### Relationships

```bash
# belongs_to, model inferred from the field name
grit generate resource Post --fields "title:string,body:richtext,category:belongs_to"

# belongs_to with an explicit target, when the field name differs from the model
grit generate resource Post --fields "title:string,author:belongs_to:User"

# many_to_many creates the join table
grit generate resource Post --fields "title:string,tags:many_to_many:Tag"

# several relations at once
grit generate resource Ticket --fields "subject:string,body:text,reporter:belongs_to:User,assignee:belongs_to:User,project:belongs_to,labels:many_to_many:Label"

# relations and files together
grit generate resource Property --fields "title:string,price:float,owner:belongs_to:User,photos:files:image,deed:file:pdf,amenities:many_to_many:Amenity"
```

Generate the parent before the child, so the target model exists when the foreign key is wired.

### Has-many line items (`--items`)

For a child that is only ever edited inside its parent — invoice lines, order items — use `--items` rather than a second resource. It generates the child model, handler and routes, adds the back-reference, and renders an editable line-items table **inside the parent's form**.

```bash
grit generate resource Invoice   --fields "number:string:auto:INV,client:belongs_to:User,status:select:draft=Draft|paid=Paid"   --items "InvoiceItem:description:string,qty:int,unit_rate:float"
```

### Other generate flags

```bash
--from <file.yaml>      # define fields in YAML instead of inline
--interactive, -i       # prompt for fields one at a time
--roles "ADMIN,EDITOR"  # restrict the generated routes to these roles
--seed --count 25       # also generate a seeder
--faker                 # make the seeded data realistic
```

### Adding a field to an existing resource

There **is** a command for this — use it rather than hand-editing:

```bash
grit g field Invoice notes:text
grit g field Invoice status:select:draft=Draft|sent=Sent|paid=Paid
grit g field Invoice paid:toggle
```

It injects the column into the Go model, the create/update Zod schemas, the TypeScript type, and the admin form and table. GORM adds the database column on the next `grit migrate`, so no migration file is written. It covers scalar, select and toggle fields — for a relationship, file, slug or array field, regenerate the resource instead.

### Removing a resource

`grit remove resource Post` deletes every generated file and reverses every injection — the inverse of generation. Use it instead of deleting files by hand.

## Authentication, roles and permissions

All of this is already built. Do not write your own login, session handling or permission checks.

### Authentication

Endpoints live under `/api/v1/auth`: `register`, `login`, `refresh`, `logout`, `forgot-password`, `reset-password`.

- **Web clients** receive the access token in an HttpOnly cookie; **native clients** (Expo, desktop) use a bearer token.
- **Refresh tokens are backed by server-side sessions** — one row per device. That is what makes sign-out real: a session can be revoked, and rotation detects replay of a stolen token. Changing a password signs out every device.
- The admin profile page lists active sessions and can revoke any single device.
- 2FA (TOTP) and SSO (OIDC + SAML) ship in the box.

Guard a route with `middleware.Auth(cfg)`. The authenticated user is on the Gin context — read it from there rather than re-parsing the token.

### Roles and permissions

There is a real `roles` table and a `user_roles` join, so **a user can hold several roles**. Permissions are strings shaped `resource.action` — `posts.edit`, `users.delete` — stored as grants on the role.

Three roles are seeded on first boot:

| Role | Grants |
|------|--------|
| `ADMIN` | `*` — the superuser wildcard, everything |
| `EDITOR` | `uploads.create`, `uploads.view`, `uploads.delete`, `users.view` |
| `USER` | none — an ordinary signed-up account holds no administrative permissions |

Seeding is idempotent and deliberately non-destructive: an existing role's grants are never overwritten on boot, so edits you make survive restarts.

### Guarding routes

```go
// by role name
protected.GET("/posts", middleware.RequireRole("ADMIN"), h.List)

// by permission — preferred, because it survives a role being renamed
protected.PUT("/posts/:id", middleware.RequireRole("perm:posts.edit"), h.Update)

// any one of several roles
protected.DELETE("/posts/:id", middleware.RequireRoles("ADMIN", "EDITOR"), h.Delete)
```

The guard passes if the caller **matches the role name OR holds the permission**. That is deliberate: it keeps older role-based routes working while you migrate to permissions.

Generate a resource with `--roles "ADMIN,EDITOR"` to have its routes guarded from the start, and `grit add role MANAGER` to register a new role across the app.

### Gating the UI

```tsx
const { can } = usePermissions()

{can('posts.edit') && <EditButton />}
```

**Never rely on the UI gate alone.** Hiding a button is a courtesy; the route guard is the control. Every privileged endpoint needs a server-side check.

### Ownership is not a permission

A permission says "this user may edit posts". It does not say "this user may edit *this* post". For user-owned records, scope the query to the caller:

```go
db.Where("id = ? AND user_id = ?", id, c.GetString("user_id")).First(&post)
```

Fetching by ID and checking ownership afterwards leaks existence, and forgetting the check altogether is the most common security bug in generated CRUD.

## CLI reference

```bash
# Create
grit new <name> [--api|--single|--double|--triple|--full|--mobile] [--frontend next|tanstack]
                [--desktop] [--expo] [--theme atlas|aurora|pulse] [--style ...] [--here]
grit new-desktop <name>            # standalone offline-first Wails app
grit init                          # add Grit to an existing directory

# Generate
grit generate resource <Name> --fields "..." [--items "..."] [--roles "..."] [--seed --count N --faker]
grit generate resource <Name> --from fields.yaml | --interactive
grit g field <Resource> <name:type[:options]>   # add one column in place
grit generate seeder <Name>
grit generate sequence <Name>
grit generate perf                 # k6 load-test scaffold
grit remove resource <Name>        # reverse a generation

# Run
grit dev                           # every app in the monorepo
grit start server | grit start client
grit up | grit down                # docker infrastructure
grit migrate                       # GORM AutoMigrate + seed roles
grit seed
grit studio                        # GORM Studio database browser
grit routes                        # every registered route with its access level

# Verify
grit test [--go] [--node] [--e2e] [--race] [--cover]

# AI
grit mcp serve [--project <dir>]   # expose this project to an AI agent over MCP

# UI blocks
grit ui list [--category ...]
grit ui add <block>...             # install a Grit UI block into your frontend

# Everything else
grit sync                          # regenerate TS types + Zod from the Go models
grit add role <NAME>
grit add web-auth                  # add auth pages to the web app
grit expose form|table <Resource>  # public shareable form / table
grit plugin add|remove|list|info <name>
grit backup | grit restore
grit compile | grit package        # build / package the desktop app
grit deploy
grit upgrade                       # bring an existing project up to current templates
grit update                        # update the CLI itself
```

### Two commands worth knowing

**`grit routes`** prints every registered route with its method, full path and access level. When you need a URL, read it from here rather than guessing — every path is prefixed `/api/v1`.

**`grit mcp serve`** exposes the project to an AI coding agent over the Model Context Protocol. Register it once:

```bash
claude mcp add grit -- grit mcp serve --project .
```

It provides three read-only tools — `grit_project_info`, `grit_list_routes`, `grit_describe_models` — answered by parsing your source, so they work without the app running and cannot change anything.

## Batteries — already included, do not re-implement

Every Grit app ships with, and you should USE rather than rebuild:

- **Auth, sessions and RBAC** — see the section above.
- **File storage** — S3/R2/B2/MinIO with presigned uploads and image processing.
- **Email** — Resend plus HTML templates.
- **Background jobs** — `asynq` on Redis, with an admin dashboard.
- **Cron** — scheduled tasks.
- **Cache** — Redis cache service and middleware.
- **AI** — Claude / OpenAI with streaming.
- **2FA / TOTP**, **SSO (OIDC + SAML)**, **hash-chained audit log**, **feature flags**, **rate limiting and security (Sentinel)**.
- **GDPR** — per-user data export and erasure, plus access reviews.
- **Automatic database backups** — manual and scheduled dumps to storage, with `grit restore`.
- **GORM Studio** — a built-in database browser at `/studio`.
- **UUIDv7 primary keys** — minted by `internal/ids`. Call `ids.New()`, never `uuid.New()`.

## Multi-tenancy

Multi-tenancy is a **plugin**, not core — most apps are single-tenant, and putting an org column on every table is a schema decision a framework should not make for you.

```bash
grit plugin add multitenant
cd apps/api && go run cmd/migrate/main.go
```

It adds `Organization` and `OrganizationMember` models, a tenant package, tenant middleware, and organization handlers and routes.

**One user belongs to many organizations, holding a different role in each.** The role lives on the membership rather than the user, which is what lets someone be an Editor in one org and a Viewer in another. It reuses the existing roles table instead of inventing a parallel permission system.

**There are no subdomains.** The active organization is resolved from an `X-Organization-ID` header or the session. Subdomain routing forces DNS and TLS decisions onto every deployment and breaks local development, so the plugin avoids it. `Organization.Slug` exists for readable URLs, not for routing.

**Scoping is automatic.** A GORM callback injects the org filter into every query on a tenant-scoped model, and stamps the org on every insert. You do not hand-write `Where("organization_id = ?")` — and critically, you cannot forget it. Opting a query OUT of scoping is explicit and therefore visible in code review, which is the right way round: a missed filter is a cross-tenant data leak.

To make one of your models tenant-scoped, embed `tenant.Owned` so it carries the org column, then let the callback do the rest. Read `internal/tenant/tenant.go` before opting anything out.

Clients must send the active organization on every request:

```ts
headers: { 'X-Organization-ID': currentOrgId }
```

## Plugins

No plugins are installed. If you later need one, plugins generate reversible code into the repo: `grit plugin list`, then `grit plugin add <name>`. Available: multitenant, impersonate, command-palette, saved-views. Removal is derived from a lockfile, so it's clean.

## API response format (follow exactly)

Success (single): `{ "data": { ... }, "message": "..." }`
Success (list): `{ "data": [ ... ], "meta": { "total": 100, "page": 1, "page_size": 20, "pages": 5 } }`
Error: `{ "error": { "code": "VALIDATION_ERROR", "message": "...", "details": { ... } } }`

Status codes: 200, 201, 400, 401, 403, 404, 422 (validation), 500.

## Conventions (match the generated code)

- **Go files** snake_case (`user_handler.go`); **structs** PascalCase; handlers stay thin, business logic in services; always handle errors (wrap with `fmt.Errorf("context: %w", err)`), never `_`-ignore them.
- **TS files** kebab-case; functional components + hooks only; all data fetching through React Query; validate inputs with Zod; no `any`.
- **Tables** plural snake_case; **API routes** plural lowercase under the version prefix (`/api/v1/posts`); **Zod schemas** PascalCase + `Schema`.
- **Styling** is Tailwind utilities + shadcn/ui. Dark, premium aesthetic — think Linear / Vercel dashboard. No custom CSS files.

## Common pitfalls — avoid these

1. Don't hand-write CRUD — run `grit generate resource`.
2. Don't delete `// grit:*` marker comments.
3. Don't put business logic in handlers — it belongs in services.
4. Don't use raw `fetch` in components — use the generated React Query hooks.
5. Don't hardcode config — use `.env` + config structs.
6. Don't add dependencies outside the stack above without a reason.
7. Don't use the Pages Router — it's the App Router (Next) or TanStack Router (Vite).

## How to work on this project

1. **Plan first.** Restate the data model as resources and their fields.
2. **Generate.** For each entity, run `grit generate resource <Name> --fields "..."`, then `grit migrate`.
3. **Customize.** Edit the generated model/service/handler/admin definition for anything the generator can't infer (relationships, custom endpoints, validation).
4. **Wire the UI.** Use the generated hooks; gate on permissions; follow the response format.
5. **Verify.** `grit test` runs the Go tests and the frontend tests and prints one report; add `--e2e` for Playwright. Then `grit dev` and click through it.

Build the product. The scaffolding, auth, admin, and batteries are already done — your job is the domain logic on top.
