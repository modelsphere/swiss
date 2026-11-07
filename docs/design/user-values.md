# Complete user values for deployment composition

A configuration editor owns one complete document. Legacy upgrade requests merge retained form keys, which cannot represent removing an override while preserving later system layers.

## Request and composition

| Contract | Behavior |
|---|---|
| `GET /api/cluster` → `capabilities.userValues: true` | The server supports complete user documents |
| `POST /api/plans` or dry-run compose with `userValues: {...}` | Replace the previous user form/edit configuration with this complete mapping |
| `userValues: {}` | Remove prior user settings and inherit the target baseline |
| `userValues` omitted | Preserve legacy merge behavior |
| Mixed `userValues` and legacy `overrides`, `overridesYAML`, `editsYAML`, or `reuseSource` | Reject the ambiguous request |

```text
source / target / release metadata -> normal upgrade validation
complete user document -----------> edit layer
                                    |
                           autoscale / system layers -> plan -> diff -> apply
```

- Store the complete document in the existing edit layer, with its existing validation/ownership semantics. Release identity and request metadata remain in the form/derived layers.
- `fromRelease` still validates model moves, resolves the running chart and carries system layers. It replaces retained user layers when `userValues` is present.
- The autoscale opt-in reads the explicit user form/edit layers together, so the same SLO choice has the same effect from structured controls or YAML. Catalog/site defaults alone do not opt a release in.
- Pin an upgrade's service identity. A conflicting `serviceId` inside the document is rejected.
- Preview uses the same composition path, including `fromRelease`; it never reconstructs the final layer order in the client.
- Authentication, deployment authorization and diff/apply revision checks remain the existing ones. Composition performs no deployment write.

## Compatibility

Clients require the advertised capability before submitting this request. Older clients retain their current behavior; older servers must be upgraded before the unified editor is enabled. No chart setting, CRD, database migration or permission change is required.

## Validation

Exercise installation, clearing an upgrade document, identity checks, preservation of unrelated user values, live autoscale carry/removal, ambiguous input rejection, and parity between planning and dry-run values. Record the results locally before publishing any change.
