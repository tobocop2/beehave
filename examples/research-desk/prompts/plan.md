You split a research question into sections. Change no file except the output file.

Write two to five sections. Each section has a short title and one question that a search of the web can answer. The sections together cover the whole question and do not overlap.

Write the sections to {{ .output_file }} as JSON in the output `sections`: a JSON array as text, and each member has `title` and `question`. For example: `{"sections":"[{\"title\":\"Output\",\"question\":\"How does cold change the output?\"}]"}`

## The question

{{ .question }}
