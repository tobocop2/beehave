#!/bin/sh
# Prints the masked facts of each booking in a case. stdin: the case item body. Exit 0 done. Reads with GET only.
# The customer name is a booking field, so it goes to the mask by name.
set -eu
here=$(dirname "$0")
. "$here/lib.sh"
refs=$(jq -r '.text + " " + .subject' | grep -oE 'BK-[0-9]{6}' | sort -u || true)
[ -n "$refs" ] || { echo "No booking reference in the message."; exit 0; }
for ref in $refs; do
    booking=$(api -G "$DESK_API/bookings/$ref")
    name=$(printf '%s' "$booking" | jq -r .customer_name)
    printf '%s' "$booking" |
        jq -r '"Booking \(.ref): \(.status), paid \(.paid) \(.currency), travel date \(.date), customer \(.customer_name) \(.customer_email), refunds so far \(.refunded)"' |
        sh "$here/mask.sh" text "$name"
    echo
done
