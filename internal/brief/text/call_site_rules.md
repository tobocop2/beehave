
## Before you report: run the call-site arm on your OWN tests

Remove the feature WHERE THE PRODUCT INVOKES IT, not inside the helper your tests call, and run the suite.
Phrase it as a sentence about the product: "the handler never applies the profile". **If the suite stays
green, your tests do not cover your change**, however many you added. Run it, report the arm, and report it RED.
