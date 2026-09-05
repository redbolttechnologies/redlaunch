# AGENTS.md

## Project

This repository contains a small self-hosted web application for managing Docker Compose applications and their configuration files on Linux VPS servers.

The application is intentionally small and operationally simple.

Primary goals:

* single self-contained application
* easy installation on a clean Linux VPS
* minimal dependencies
* server-rendered HTML
* HTMX for partial page updates
* Alpine.js only for small client-side interactions
* SQLite for persistence
* Docker Compose for application/container management

Do not introduce unnecessary frameworks, abstractions, services, or dependencies.

## Architecture

Keep the architecture layered:

HTTP/UI
→ application services
→ infrastructure

HTTP handlers must not directly execute Docker commands or access the database.

Docker/Compose operations belong behind dedicated interfaces/services.

Persistence belongs behind the database/repository layer.

Business logic should remain independent of HTTP and HTML.

## Repository conventions

Before making a significant change:

1. Inspect the relevant existing code.
2. Identify the layer that owns the behavior.
3. Make the smallest change that solves the problem.
4. Run focused tests.
5. Run the full verification suite when appropriate.

Do not refactor unrelated code while implementing a feature.

Do not introduce abstractions until they are justified by existing duplication or complexity.

Prefer simple functions and explicit control flow over clever abstractions.

## Agent workflow

For non-trivial tasks:

1. Explore the repository.
2. Explain the intended approach briefly.
3. Identify affected files.
4. Implement the smallest coherent change.
5. Run tests and static checks.
6. Review the resulting diff.
7. Report what was changed and what was verified.

Do not modify unrelated files.

Do not silently change architecture or dependencies.

If requirements are ambiguous and the ambiguity can materially affect architecture, stop and ask.

## Verification

Before considering a change complete, run the appropriate checks.

At minimum, normally run:

```
make test
make lint
```

For Docker/Compose changes also run:

```
docker compose config
```

For changes affecting the production image:

```
docker build .
```

Prefer focused tests first, followed by the complete test suite.

Never claim that a test or command was run if it was not actually run.

## Docker and Compose

Docker is a privileged system interface.

Treat Compose files, Docker commands, container names, volume paths, network configuration, bind mounts, and environment variables as security-sensitive.

Never construct shell commands by concatenating untrusted user input.

Prefer exec-style process execution with explicit arguments.

Never use `sh -c` for user-controlled values.

Validate paths before accessing the filesystem.

Do not allow a user-controlled path to escape its configured project directory.

Do not expose the Docker socket or Docker API directly through HTTP.

Use the dedicated Docker/Compose service layer.

When changing Compose behavior, validate generated configuration with:

```
docker compose config
```

Do not assume a Compose file is harmless. Compose configuration can grant containers significant access to the host.

## Docker resource layout and management labels

Follow these domain rules for every managed Docker resource:

* Every container must have the label `redlaunch.managed=true`. This applies to application containers and core component containers alike.
* Whenever a managed container is created, its project must contain separate `vars.env` and `secrets.env` files, and the service must reference both files through `env_file`. This applies to application containers and core component containers alike.
* Store applications under the dedicated `applications` directory. Each application must have its own directory containing `compose.yml`; all other application-specific files, including `vars.env`, `secrets.env`, configuration files, and vaults, belong in that application directory.
* Store core components under the dedicated `core` directory. Each component must have its own directory containing its `compose.yml` and any component-specific configuration files. For example, the reverse proxy belongs in `core/proxy/`.
* Do not place application files directly in `applications` or core-component files directly in `core`; always use the per-resource directory structure.
* When naming a container, always use a prefix. The default prefix is `redbolt-`.

## Environment files

Every managed container project must keep non-secret environment variables in
`vars.env` and credentials or other secrets in `secrets.env`. Both files must
be created for every managed container project, even when one or both are
initially empty, and every managed service must load both files through
Compose's `env_file` setting.

Never:

* commit real `.env`, `vars.env`, or `secrets.env` files
* print secrets to logs
* include secret values in error messages
* expose environment-file contents through HTTP responses
* include secrets in test snapshots
* send secrets to the browser unless explicitly required

Keep `.env.example`, `vars.env.example`, `secrets.env.example`, or equivalent safe sample configuration separate from real environment files.

When displaying environment variables in the UI, support masking sensitive values.

Preserve the semantics of Docker Compose environment-variable interpolation.

Do not casually rewrite `.env`, `vars.env`, or `secrets.env` files using generic string replacement.

Use a dedicated parser/writer and preserve comments and formatting where practical.

Docker Compose has non-trivial environment-variable precedence and interpolation rules; do not invent alternative semantics without an explicit requirement.

## Security

Assume all user-controlled input is hostile.

Pay particular attention to:

* command injection
* path traversal
* arbitrary filesystem access
* Docker privilege escalation
* malicious Compose configuration
* CSRF
* XSS
* SSRF
* authentication bypass
* authorization bypass
* secret disclosure
* log injection

HTML must always be escaped unless the content is explicitly trusted.

Do not render raw user-controlled HTML.

Never trust filenames, project names, container names, environment variable names, or Compose configuration supplied by a user.

Security-sensitive changes require tests.

## HTTP/UI

Prefer server-rendered HTML.

Use HTMX for server interaction and partial page updates.

Use Alpine.js only for local browser state and interactions that do not belong on the server.

Do not introduce a client-side application framework.

Keep templates small.

Use one clear heading for each page or dialog; do not duplicate a heading with an equivalent kicker or label (for example, avoid repeating "first-run configuration" above a setup heading).

Prefer reusable partials over duplicated HTML.

HTMX endpoints should return the smallest useful HTML fragment.

Forms should work correctly with normal HTTP semantics wherever practical.

## Database

SQLite is the persistence layer.

Keep schema changes explicit and migration-based.

Do not modify an existing migration after it has been used outside local development.

Schema changes must include appropriate tests.

Use transactions for operations that must be atomic.

Avoid SQLite patterns that create unnecessary write contention.

Do not store secrets in the database unless explicitly required.

## Dependencies

Prefer the standard library when it provides a reasonable solution.

Every new dependency must have a clear justification.

Do not add a dependency merely to save a few lines of code.

Before adding a dependency, consider:

* maintenance status
* security history
* binary size
* API stability
* whether the standard library is sufficient

## Testing

New behavior should normally have tests.

Prefer:

* unit tests for pure logic
* integration tests for SQLite/database behavior
* integration tests for Docker/Compose adapters where practical
* HTTP tests for handlers
* end-to-end tests only where they provide meaningful additional coverage

Tests must be deterministic.

Do not depend on a developer's personal Docker projects, filesystem paths, or `.env` files.

Use temporary directories and isolated test databases.

## Git

Keep commits small and logically focused.

Do not rewrite existing commits unless explicitly requested.

Do not commit:

* `.env`
* credentials
* private keys
* local databases
* Docker volumes
* generated build artifacts
* editor-specific files

Review `git diff` before completing substantial work.

## Documentation

Update documentation when behavior, installation, configuration, or operational procedures change.

Prefer concise documentation with executable examples.

Do not document behavior that the code does not actually implement.

## Important principle

Keep the application boring.

Simple code, explicit behavior, minimal dependencies, strong validation, and easy installation are more important than architectural sophistication.

## Definition of done

A task is complete only when:

- implementation is complete
- relevant tests exist
- tests pass
- lint passes
- Docker/Compose validation passes when relevant
- no secrets were introduced
- git diff has been reviewed
- documentation was updated when behavior changed

## UI Design System

The application UI MUST follow the **Pietro Schirano shadcn/ui design system for Figma**.

Figma design reference:
**shadcn/ui design system by Pietro Schirano**

https://www.figma.com/design/lvLMxnaON9uDRDHzwlmekQ/-shadcn-ui---Design-System--Community-?node-id=4-6598&p=f&t=OXjZfR9lYG7FdKr8-0

The implementation guidelines in DESIGN.md are normative for all UI work. Read and follow DESIGN.md before changing templates, styles, components, or interaction states.

The Figma design system is the visual reference for:

* component appearance
* component variants
* spacing
* typography
* colors
* borders
* radius
* shadows
* states
* layout patterns

Do not treat the Figma system as general inspiration. Treat it as the project's design system.

### Design-system hierarchy

Follow this hierarchy:

1. Existing Docker Manager components
2. Existing shadcn/ui component patterns
3. Pietro Schirano's Figma design system
4. Application-specific extensions

Do not introduce a new visual pattern when an existing pattern can be reused.

### Component reuse

Before creating a UI component:

1. Check whether an existing Docker Manager component can be reused.
2. If not, check whether the component corresponds to a standard shadcn/ui component.
3. Reuse the existing component and adapt its content rather than creating a visually similar component.
4. Only create a new component when the existing component cannot express the required behavior.

New components must follow the visual language of the Pietro Schirano shadcn/ui design system.

### Visual consistency

Never introduce arbitrary:

* colors
* font sizes
* spacing values
* border radii
* shadows
* border styles
* control heights

when an existing design-system token can be used.

Do not reproduce a Figma screen by hard-coding individual pixel values.

Use semantic design tokens and reusable components.

### Figma → implementation mapping

When implementing a Figma design:

1. Identify the Figma components used by the design.
2. Identify their variants and properties.
3. Identify the relevant Figma variables/tokens.
4. Map those components to the application's existing components.
5. Map Figma variables to the application's design tokens.
6. Implement the page by composing existing components.
7. Only introduce new components when necessary.
8. Verify all important component states.

The goal is not merely to make the implementation visually similar to the Figma design.

The goal is for the implementation and Figma design to behave as two representations of the same design system.

### shadcn principles

Follow the principles of shadcn/ui:

* open code
* composition
* reusable primitives
* semantic design tokens
* accessible components
* predictable variants
* minimal visual duplication

Do not create a conventional monolithic UI component library.

Prefer small composable components.

### Application-specific components

The application may extend the shadcn design system with Docker-specific components such as:

* ContainerStatus
* ProjectStatus
* EnvironmentVariable
* ImageReference
* PortMapping
* VolumeReference
* NetworkReference
* LogViewer
* ComposeEditor
* DockerResourceBadge

These components MUST visually inherit from the shadcn design system.

They must not introduce an unrelated visual language.

### Technical UI

Technical values should use monospace typography where appropriate.

Examples:

* container names
* image names
* Docker commands
* ports
* filesystem paths
* environment variable names
* environment variable values
* Compose YAML
* container logs

Technical information should remain visually compact and scannable.

### Status colors

Use semantic status colors consistently:

* running → success
* stopped → neutral
* warning/unhealthy → warning
* failed → destructive
* informational → info

Never communicate status using color alone.

Always combine status color with text, iconography, or another visual indicator.

### Density

This is an infrastructure/developer tool.

Prefer compact, information-dense layouts over large marketing-style layouts.

Do not use:

* oversized cards
* excessive whitespace
* decorative gradients
* unnecessary illustrations
* excessive shadows
* excessive rounded containers
* unnecessary animations

### HTMX and Alpine.js

The application uses server-rendered HTML with HTMX and Alpine.js.

Do not introduce React, Vue, Svelte, or another client-side UI framework.

Use HTMX for server interactions and partial page updates.

Use Alpine.js only for local client-side interaction and state.

The visual behavior of HTMX interactions must remain consistent with the Figma design system.

### Visual QA

After implementing a significant UI change, review it against the design system.

Check:

* typography
* spacing
* component variants
* colors
* borders
* radius
* density
* alignment
* hover states
* focus states
* disabled states
* loading states
* error states
* responsive behavior
* dark mode
* accessibility

If an implementation requires many one-off CSS rules, stop and reconsider whether an existing design-system component or token should be used.

### Golden UI Rule

**Never invent UI styling when the design system already provides the answer.**

Before writing CSS or creating a new UI component, determine whether the required visual treatment already exists in:

1. the Docker Manager component system,
2. the shadcn/ui component system,
3. the Pietro Schirano Figma design system.

Reuse or compose existing patterns whenever possible.

A new visual pattern is a design-system change, not merely an implementation detail.
