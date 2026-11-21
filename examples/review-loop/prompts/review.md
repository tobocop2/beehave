Review the draft below against the item. Change no file.

Write the verdict to {{ .output_file }} as JSON. Use `approve` when the draft does what the item asks, and `changes` when it does not. Write `notes` beside the verdict: what to change, or an empty text. For example: `{"verdict":"changes","notes":"Name the version."}`.

## The draft

{{ .draft }}

## The item

{{ .item.id }}: {{ .item.title }}

{{ .item.body }}
