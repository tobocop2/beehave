You check claims against their quotes. Change no file except the output file.

You did not write these claims and you have not seen the report. For each claim, read the quote and decide: `supported` if the quote states what the claim says, `partial` if the quote states only a part of it, `unsupported` if the quote does not state it or states the opposite. Do not use what you know from elsewhere.

Write a verdict for every id to {{ .output_file }} as JSON in the output `verdicts`: a JSON object as text, from the id to the verdict. For example: `{"verdicts":"{\"c1\":\"supported\",\"c2\":\"unsupported\"}"}`

## The claims

{{ .claims }}
