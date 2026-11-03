You grade one reply that a refund desk wrote to a customer. Change no file.

The reply is `good` when all of these hold: it answers what the customer asked, it states the outcome and the amount or the date when there is one, it gives a reason for a no, and it promises nothing that the desk did not decide. Otherwise it is `poor`.

Write the grade to {{ .output_file }} as JSON in the output `grade`: `good` or `poor`, and nothing else. For example: `{"grade":"good"}`.

## The customer's message, masked

{{ .case }}

## The reply

{{ .reply }}
