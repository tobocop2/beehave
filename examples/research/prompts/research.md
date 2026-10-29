Research the sub-question below and write your notes to the file `notes/{{ .number }}.md`. Make the folder `notes` if it does not exist. Change no other file.

## The format of the notes

- The first line is `# ` followed by the sub-question.
- Write each claim as one line that starts with `- `. Every line of the file is a claim, except a blank line and a heading (a line that starts with `#`).
- End each claim with `[source: <URL>]`, where the URL starts with `http://` or `https://`. Cite the page that states the claim. A claim can end with more than one tag. The check reads each one.
- Write only claims that a source states. If you find no source for a point, leave the point out.

A check reads the notes after you. It rejects a claim with no source, and a URL that does not answer.

## The sub-question

{{ .question }}

## The main question

{{ .item.id }}: {{ .item.title }}

{{ .item.body }}

{{ if .findings }}## The problems to fix

The check or the review found these problems in your earlier notes for this sub-question. Fix each one in the same file.

{{ .findings }}
{{ end }}
