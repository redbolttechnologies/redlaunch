# DESIGN.md

## Design System

The UI follows a shadcn/ui-inspired design system.

Figma is the visual design reference.

The implementation must preserve the design system's visual language across all pages.

Do not create one-off visual styles when an existing design token or component can be reused.

## Design principles

The interface should be:

* clean
* restrained
* professional
* dense enough for a developer/admin tool
* highly readable
* consistent
* accessible
* responsive
* visually quiet

Avoid:

* gradients unless explicitly specified
* excessive rounded cards
* decorative illustrations
* excessive shadows
* excessive colors
* oversized typography
* unnecessary animations
* visual noise
* "marketing website" aesthetics

This is an operational developer tool, not a marketing website.

## Visual hierarchy

Use typography, spacing, borders and subtle background differences to establish hierarchy.

Do not rely on color alone.

Primary actions should be visually obvious.

Destructive actions must use destructive styling and normally require confirmation.

Secondary actions should remain visually subordinate to primary actions.

## Color

Use semantic design tokens rather than hard-coded colors.

Required semantic tokens include:

* background
* foreground
* card
* card-foreground
* popover
* popover-foreground
* primary
* primary-foreground
* secondary
* secondary-foreground
* muted
* muted-foreground
* accent
* accent-foreground
* success
* destructive
* destructive-foreground
* code
* code-foreground
* border
* input
* ring

Never introduce a new color directly in a component.

If a new semantic color is required, update the design system first.

## Typography

Use the design system typography scale consistently.

Do not use arbitrary font sizes.

Use typography hierarchy for:

* page titles
* section titles
* labels
* body text
* descriptions
* metadata
* code/log output

Technical content such as container names, image names, commands, paths and environment variables may use a monospace font.

## Spacing

Use the design system spacing scale.

Do not invent arbitrary spacing values.

Prefer the existing spacing tokens/utilities.

Consistency is more important than pixel-perfect local optimization.

## Components

Prefer existing components before creating new ones.

Expected base components include:

* Button
* Input
* Textarea
* Select
* Checkbox
* Switch
* Badge
* Card
* Dialog
* Dropdown Menu
* Tooltip
* Tabs
* Table
* Alert
* Alert Dialog
* Toast
* Breadcrumb
* Sidebar
* Command
* Skeleton

New components should compose existing primitives.

Do not create visually similar components with slightly different styling.

## States

Every interactive component should consider:

* default
* hover
* focus
* active
* disabled
* loading
* error
* success
* empty

Use the same state conventions throughout the application.

## Tables

Tables are important to this application.

Use consistent:

* header height
* row height
* cell padding
* alignment
* typography
* hover behavior
* status indicators
* action placement

Do not create a custom table style for individual pages.

## Forms

Forms should have consistent:

* label placement
* field spacing
* input height
* help text
* validation messages
* error states
* submit/cancel actions

Destructive operations must be visually distinct.

## Docker-specific UI

Docker-related information should use compact, technical presentation.

Examples:

* container names
* image names
* ports
* volumes
* networks
* Compose project names
* environment variables
* logs
* commands

Use monospace typography where appropriate.

Long technical values should be truncatable with a way to inspect the complete value.

## Logs

Logs should be displayed in a dedicated technical surface.

Use:

* monospace font
* appropriate line height
* preserved whitespace
* scrolling
* clear separation from normal application UI

Do not style logs like normal prose.

## HTMX

The UI is server-rendered.

Use HTMX for:

* partial updates
* form submissions
* table updates
* actions
* navigation enhancements

HTMX interactions must preserve the same visual design system as full-page rendering.

Loading states should use the existing design-system components.

## Alpine.js

Use Alpine.js only for local browser state.

Examples:

* dialogs
* dropdowns
* tabs
* client-side toggles
* temporary UI state

Do not implement business logic in Alpine.js.

## Responsive design

The application must work on:

* desktop
* laptop
* tablet
* mobile

Desktop is the primary target.

Do not simply shrink desktop layouts on mobile.

Hide, collapse or reorganize secondary information when necessary.

## Accessibility

Follow accessible HTML semantics.

All interactive elements must be keyboard accessible.

Focus states must remain visible.

Do not use color as the only indication of state.

Forms must have proper labels.

Dialogs and menus must have appropriate keyboard behavior.

## Design consistency rule

Before creating a new visual pattern, search the existing application for an equivalent component or pattern.

Reuse it if possible.

If it cannot be reused, extend the design system rather than creating a one-off implementation.

## Figma

Figma is the visual reference for intended layouts and component appearance.

When implementing a Figma design:

1. Identify the existing design-system components.
2. Identify the design tokens being used.
3. Reuse the corresponding application components.
4. Match layout, spacing, typography and states.
5. Do not reproduce the design by hard-coding arbitrary pixel values when an existing token can express the same intent.

The implementation should look like the same product as the Figma design, not merely resemble the screenshot.
