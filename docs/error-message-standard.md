# BSA Error Message Standard

Date: 2026-03-13
Status: Active

## Why
Botster Spine errors are often surfaced inside other BSA components (for example, OpenClaw chat/tool output).
Without source prefixes, cross-component debugging becomes ambiguous.

## Required Format
All broker-originated user-visible error messages MUST include a component prefix:

`[BSA:SPINE/<subcomponent>] <message>`

Examples:
- `[BSA:SPINE/DASHBOARD] Not authenticated`
- `[BSA:SPINE/HUB] Actuator not connected`
- `[BSA:SPINE/TAPSTREAM] Streaming not supported`

## Scope
Apply this format to:
- API JSON error payloads (`jsonError` call sites)
- WebSocket error/status payloads intended for upstream display
- Any broker text error that may appear outside broker-local logs

## Naming Guidance
Use stable uppercase component labels:
- `SPINE/DASHBOARD`
- `SPINE/HUB`
- `SPINE/TAPSTREAM`
- add others as needed (e.g., `SPINE/AUTH`, `SPINE/PROXY`)

Keep messages concise and actionable after the prefix.

## Rollout
- New/edited code paths: required immediately
- Legacy paths: migrate incrementally during touch-ups and bugfix passes
