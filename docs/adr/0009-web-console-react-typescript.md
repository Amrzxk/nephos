# ADR-0009: Web console in React and TypeScript

- **Status:** Accepted
- **Date:** 2026-09-17

## Context

Beginners need a console, not just a CLI. The console needs:

- a **live topology map** with nested containment (VPC ⊃ availability zone ⊃ subnet ⊃ instances), gateways, and routes;
- real-time status updates, create wizards, browser terminals, a lab panel, and an overlay showing why traffic is blocked.

`nephosd` serves the console locally. It should attract contributors, and it's part of a portfolio project.

## Options considered

### Option A: React + TypeScript single-page app built with Vite
- Pros:
  - Largest ecosystem and contributor pool.
  - React Flow (`@xyflow/react`, MIT) handles interactive node graphs with group nodes.
  - `elkjs` lays out nested (compound) graphs automatically.
  - Also available: xterm.js, TanStack Query, accessible Radix primitives.
  - Builds to static files that are easy to embed.
- Cons: a JavaScript toolchain; frontend library churn.

### Option B: Svelte / SvelteKit
- Pros: smaller bundles; simpler reactivity; Svelte Flow exists.
- Cons: smaller contributor pool; fewer mature components for a complex app.

### Option C: Server-rendered Go (templ + HTMX)
- Pros: one language; no client state management.
- Cons: the topology map, terminals, and live overlays need substantial JavaScript anyway, so the project ends up with two paradigms.

### Option D: Vue / Nuxt
- Pros: pleasant developer experience; Vue Flow exists.
- Cons: no advantage over React for this app, and a smaller ecosystem for graph editing.

## Decision

Build the console as a **React + TypeScript (strict mode) single-page app with Vite**, in `web/`.

- **Data:**
  - TanStack Query for server state; React Router for routing.
  - API types generated from `api/openapi.yaml` (`openapi-typescript` + `openapi-fetch`).
  - Server-sent events invalidate queries in real time.
- **UI:** shadcn/ui (Radix primitives + Tailwind CSS) with Nephos's own visual identity. No AWS logos, icons, or trade dress.
- **Topology:**
  - React Flow group nodes (VPC → AZ → subnet) with instances, NAT gateways, internet gateways, and Elastic IPs as nodes.
  - ELK layered layout with hierarchy support.
  - Status shown with icon + text + color, never color alone.
  - An explain overlay highlights each hop and the blocking rule.
- **Terminals:** xterm.js over WebSocket for the serial console and Instance Connect ([ADR-0006](0006-learner-access-through-simulated-internet.md)).
- **Delivery:** the production build is embedded into `nephosd` with Go's `embed` and served at `/` on the same origin as the API, so no CORS is needed.
- **Tests:** Vitest for units; Playwright smoke tests against a running appliance in CI.

## Consequences

- Positive: a mature graph and terminal stack; a large contributor pool; one binary to ship.
- Negative / costs:
  - Web development needs Node.js. Go-only contributors build against a small placeholder `web/dist` so `go build` never requires Node.
  - `elkjs` is licensed EPL-2.0, which brings notice obligations ([RISKS](../RISKS.md) P5).
- Follow-ups: M3 may add a throwaway read-only topology page for debugging; the real console is built in M6.

## Validation

M6 acceptance criteria:

- The topology renders 5 VPCs and 50 instances without jank.
- Changes made with the CLI appear within 1 second.
- A new user completes the "first instance" flow entirely in the browser.
- Playwright smoke tests pass in CI.
