Write a short summary of the notes in the folder `notes`. Change no file.
Answer the main question in at most five sentences. Use only what the notes state. Do not add a claim, a number or a source that is not in the notes.
A reviewer reads this summary after you and checks each statement against the notes.

Write the summary to {{ .output_file }} as JSON in the output `summary`. For example: `{"summary":"A is faster than B in the tests that the notes cite."}`.

## The main question

{{ .item.id }}: {{ .item.title }}

{{ .item.body }}

{{ if .findings }}## The problems to fix

The review found these problems in your earlier summary. The notes are already revised. Write a new summary of the notes as they are now, and remove each statement that the notes do not state.

{{ .findings }}
{{ end }}
