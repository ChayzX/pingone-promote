# ADR 0006: HCL I/O with hashicorp/hcl/v2, app-canonical files, terraform fmt gate

- Status: Accepted
- Date: 2026-11-03
- Issues: #60, #64

## Decision

**Parse.** `hclsyntax.ParseConfig`, then `attr.Expr.Value(nil)`. A nil
evaluation context is not enough by itself: for expressions and literal-only
templates still evaluate. The parser therefore also walks the syntax tree and
accepts only:

- literals;
- literal-only strings and heredocs;
- tuples;
- objects with identifier or literal keys;
- a negative number sign.

It rejects duplicate keys and blocks, bounds input size (32 MiB) and nesting
depth, and decodes strictly into typed Go structs. Unknown attributes, missing
required attributes and wrong types are errors. Error messages carry positions
and paths, never values.

**Write.** The writer produces deterministic text: managed header, sorted and
quoted map keys, one attribute per line, sorted sets, and canonical JSON
heredocs. The text is then passed through `hclwrite.Format`, the formatter
`terraform fmt` uses. Write → Parse → Write is byte-identical, which fuzz
tests confirm.

**Canonical files.** The file is app-canonical, and the managed header says
that hand comments are not preserved. `check` reports any file that differs
from the canonical re-rendering.

**terraform fmt gate.** Before generated tfvars are committed or pushed, the
tool runs the real formatter.
`tfvars.FormatWithTerraform(ctx, binPath, content)`:

- pipes the content through `terraform fmt -` or `tofu fmt -`, with a
  configurable binary path, or terraform then tofu looked up on PATH;
- applies a 30 s timeout;
- gives the child a sealed environment: nothing inherited, an empty CLI
  config, checkpoint disabled, and every proxy pointed at the loopback
  discard port so any network attempt fails;
- requires the output to equal both the input and `hclwrite.Format(input)`.

It returns typed errors: `FormatterNotFoundError`, `FormatterFailedError` and
`FormatMismatchError` (with the first differing line).
`pingone-promote check --terraform-fmt[=PATH]` runs the same gate. The GitLab
submission path calls it in a later PR (#9).

## Alternatives

- `hclwrite` editing in place to preserve comments: rejected. The output
  would depend on edit history, not just on content.
- Trusting `hclwrite.Format` alone: rejected by the user. The actual
  `terraform fmt` binary is the authority the pipeline will apply.

## Consequences

- Plain HCL strings are NFC-normalized by HCL itself, both on read and in
  Terraform. JSON heredoc content is lossless.
- Generating a file needs a terraform or tofu binary when the fmt gate is
  enabled. When it is missing, the error says so.
