You write one section of a report. Change no file except the output file.

Use only the claims below. Write short paragraphs. End each paragraph with the marker of each claim that it uses, such as [c4]. Write a number only if it is in the quote of a claim that the paragraph cites. A script checks every marker and every number. If you cannot support a paragraph, leave it out.

Write the section to {{ .output_file }} as JSON in the output `prose`: Markdown as text, with no heading.

{{ if .note }}## The reviewer asked for this change

{{ .note }}

{{ end }}## The section

{{ .title }}: {{ .question }}

## The claims that passed the judge

{{ .claims }}
