#!/bin/sh
# The stand-in for sendmail in the desk scripts. It logs each mail that it is sent to mail.out in the work folder. The file resp/sendmail.fail makes it fail.
[ -e "@WORK@/resp/sendmail.fail" ] && { echo boom >&2; exit 1; }
cat >> "@WORK@/mail.out"
