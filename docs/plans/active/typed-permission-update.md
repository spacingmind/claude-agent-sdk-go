# Typed PermissionUpdate (phase 4d)

Closes the last audit gap: phase 1's `can_use_tool` handling accepts
`updatedPermissions []map[string]any` from `PermissionPolicy.Decide` —
an untyped passthrough. The Python reference has a typed `PermissionUpdate`
dataclass with selective-field `to_dict()`/`from_dict()` marshaling (only
the fields relevant to `.Type` are sent on the wire). This phase ports
that as a proper Go type with custom JSON marshaling.

## Acceptance Criteria

1. ```go
   type PermissionRuleValue struct {
       ToolName    string
       RuleContent string // empty = omitted
   }
   type PermissionUpdate struct {
       Type        string // "addRules" | "replaceRules" | "removeRules" | "setMode" | "addDirectories" | "removeDirectories"
       Rules       []PermissionRuleValue // addRules/replaceRules/removeRules only
       Behavior    string                 // "allow" | "deny" | "ask"; addRules/replaceRules/removeRules only
       Mode        string                 // setMode only
       Directories []string               // addDirectories/removeDirectories only
       Destination string                 // optional on every variant; "userSettings" | "projectSettings" | "localSettings" | "session"
   }
   ```
2. `PermissionUpdate` implements `MarshalJSON` that emits only the fields
   relevant to `.Type` (matching Python's `to_dict()`): for the three
   rules-variants, emit `{"type","rules":[{"toolName","ruleContent"?}],"behavior","destination"?}`;
   for `"setMode"`, emit `{"type","mode","destination"?}`; for the two
   directory-variants, emit `{"type","directories","destination"?}`.
   `Destination` is included whenever non-empty regardless of `.Type`.
   Fields not relevant to the given `.Type` must not appear on the wire
   even if they happen to be non-zero-valued on the Go struct (garbage in,
   ignored on the way out — don't validate/error on mismatched fields,
   just don't serialize them).
3. `PermissionPolicy.Decide`'s return signature changes
   `updatedPermissions []map[string]any` → `updatedPermissions []PermissionUpdate`
   (a breaking change to an interface that only exists since phase 1,
   acceptable — see Decisions). Update `AutoApprovePolicy`/`AutoDenyPolicy`
   to match (`nil` for the new field, unchanged behavior otherwise).
4. `engine.go`'s `can_use_tool` response-building marshals
   `updatedPermissions` via each `PermissionUpdate`'s `MarshalJSON` (a
   plain `json.Marshal` call on the slice already does this correctly once
   AC 2 is implemented — no special-casing needed at the call site beyond
   using the new type).

## Test Scenarios

- Marshal a rules-variant `PermissionUpdate` (`Type:"addRules"`) with
  `Mode`/`Directories` also non-empty (deliberately, to prove they're
  dropped) → the resulting JSON has only `type`/`rules`/`behavior`, no
  `mode`/`directories` keys.
- Marshal a `"setMode"` variant → only `type`/`mode` (plus `destination`
  if set); same drop-irrelevant-fields check.
- Marshal a directory variant → only `type`/`directories` (plus
  `destination` if set).
- `Destination` appears on every variant when set, absent on every variant
  when empty.
- `PermissionRuleValue.RuleContent` omitted when empty, present when set.
- End-to-end: a `PermissionPolicy` implementation that returns 2
  `PermissionUpdate`s of different types on allow → the fake-CLI-captured
  `control_response`'s `updatedPermissions` array has the correct
  per-element shape for each (extends phase 1's
  `TestClient_CanUseToolRoundTripFields`).

## Decisions

- This changes `PermissionPolicy.Decide`'s signature again (it already
  changed once in phase 1, from 4 to 6 return values) — acceptable since
  the whole port is still actively evolving and no external consumer
  beyond this repo's own tests exists yet; not treated as a stability
  concern requiring a deprecation path.

## Progress

Not started.

## Validation

Not yet applicable.
