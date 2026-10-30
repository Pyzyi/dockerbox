## Summary

<!-- What does this change and why? Link issues with "Closes #…". -->

## Type of change

- [ ] Feature
- [ ] Bug fix
- [ ] Security hardening
- [ ] Documentation / process
- [ ] Refactoring / chore

## Quality checklist (ISO/IEC 25010)

- [ ] **Functional suitability** – behaviour covered by tests
- [ ] **Performance efficiency** – no new idle cost per agent box, no unbounded loops/polling
- [ ] **Compatibility** – only touches Docker objects with `dockerbox.managed=true`
- [ ] **Interaction capability** – UI change works on desktop and mobile, EN + DE strings
- [ ] **Reliability** – errors handled and rolled back, no panics on bad input
- [ ] **Security** – threat model reviewed; policy/egress changes have tests
- [ ] **Maintainability** – `make check` passes, docs/ADRs updated
- [ ] **Flexibility** – new settings documented in `docs/configuration.md`
- [ ] **Safety** – resource limits still enforced

## Privacy

- [ ] No personal data, secrets, hostnames or private IPs added

## Testing

<!-- How was this verified? Paste relevant output. -->
