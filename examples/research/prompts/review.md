Try to refute the notes in the folder `notes`. Change no file.
Read each claim and its source. Open the source. Report a claim that the source does not state, a claim that the source contradicts, and a sub-question that the notes do not answer.

The summary below goes to the top of the report. Check it too. Report each statement in it that the notes do not state, and each statement that the notes contradict. Start the finding with `summary:`. Do not trust the summary: read the notes.

Write the verdict to {{ .output_file }} as JSON. Use `approve` when you find no problem, and `changes` when you find one. Write a `findings` list beside the verdict. Give each finding a `class` and a `text`. Use the class `blocking` for a claim that is wrong or unsupported, and `minor` for a weak point. For example: `{"verdict":"changes","findings":[{"class":"blocking","text":"notes/2.md: the source does not state the claim"}]}`. Write an empty list when you find nothing: `{"verdict":"approve","findings":[]}`.

Write the output `open` beside them. It is one text with each finding on its own line, with the file and the claim. The report shows this text as the open findings. Write an empty text when you find nothing. For example: `{"verdict":"changes","findings":[{"class":"blocking","text":"notes/2.md: the source does not state the claim"}],"open":"notes/2.md: the source does not state the claim"}`.

## The summary

{{ .summary }}

## The question

{{ .item.id }}: {{ .item.title }}

{{ .item.body }}
