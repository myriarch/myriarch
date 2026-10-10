# 0005. Templating with double brackets and typed whole-expression evaluation

- Status: Accepted
- Date: 2026-10-07
- Related: [ADR-0004](0004-server-side-rendering-to-one-content-addressed-config.md)

## Context

Blueprints are collector configurations with parameters. The collector has its own substitution syntax: `${env:NAME}`, `${file:path}`, `${VAR}` and the escape `$$`. The server must not interpret or damage it, because secrets are referenced that way and resolved on the host ([security model](../security/model.md#secrets-handling)).

Parameters have types: numbers, booleans, durations, lists and maps. A rendered configuration where `limit_percentage` becomes the string `"75"` or a list becomes a joined text would be wrong or invalid.

## Decision

Templates use the delimiters `[[` and `]]` (Go `text/template`, with an error on missing keys) and are evaluated per string value, after the YAML has been parsed.

- A string that consists of exactly one `[[ expression ]]` is evaluated as a typed whole expression: the result keeps its type (number, boolean, list, map).
- Any other string is interpolated and stays a string. Lists and maps are never converted to text implicitly; the author uses an explicit function such as `join` or `toJson`.
- The collector syntax `${...}` and `$$` is left untouched.
- Templates have no access to the environment, files or the network, and are bounded in output size, nesting and run time ([security model](../security/model.md#gitops-input-limits)).

## Consequences

### Positive

- No collision with the collector's substitution syntax.
- Types survive rendering, so numeric and list-valued settings are valid YAML values in the result.
- Because templates act on parsed values, a parameter value cannot change the structure of the document.

### Negative

- A YAML value that starts with `[[` must be quoted, otherwise YAML reads a flow sequence. The validator reports a typical symptom as a distinct issue code.
- Authors must know the difference between whole-expression and interpolated strings.
- Templating is a restricted subset of `text/template`; some constructs (`define`, `template`, `block`) are not available.

## Alternatives considered

- **Text templating before the YAML parse**: exposes the configuration to YAML injection and indentation mistakes.
