package signalwire

import (
	"context"
	b64 "encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/trufflesecurity/trufflehog/v3/pkg/common"
	"github.com/trufflesecurity/trufflehog/v3/pkg/detectors"
	"github.com/trufflesecurity/trufflehog/v3/pkg/pb/detectorspb"
)

type Scanner struct{}

// Ensure the Scanner satisfies the interface at compile time
var _ detectors.Detector = (*Scanner)(nil)

var (
	client = common.SaneHttpClient()

	//Make sure that your group is surrounded in boundry characters such as below to reduce false positives
	keyPat = regexp.MustCompile(detectors.PrefixRegex([]string{"signalwire"}) + `\b([0-9A-Za-z]{50})\b`)
	idPat  = regexp.MustCompile(detectors.PrefixRegex([]string{"signalwire"}) + `\b([0-9a-z]{8}-[0-9a-z]{4}-[0-9a-z]{4}-[0-9a-z]{4}-[0-9a-z]{12})\b`)
	urlPat = regexp.MustCompile(`\b([0-9a-z-]{3,64}\.signalwire\.com)\b`)
)

// Keywords are used for efficiently pre-filtering chunks.
// Use identifiers in the secret preferably, or the provider name.
func (s Scanner) Keywords() []string {
	return []string{"signalwire"}
}

// FromData will find and optionally verify Signalwire secrets in a given set of bytes.
func (s Scanner) FromData(ctx context.Context, verify bool, data []byte) (results []detectors.Result, err error) {
	dataStr := string(data)

	matches := keyPat.FindAllStringSubmatch(dataStr, -1)
	idMatches := idPat.FindAllStringSubmatch(dataStr, -1)
	urlMatches := urlPat.FindAllStringSubmatch(dataStr, -1)

	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		resMatch := strings.TrimSpace(match[1])

		for _, idMatch := range idMatches {
			if len(idMatch) != 2 {
				continue
			}
			resID := strings.TrimSpace(idMatch[1])

			for _, urlMatch := range urlMatches {
				if len(urlMatch) != 2 {
					continue
				}
				resURL := strings.TrimSpace(urlMatch[1])

				s1 := detectors.Result{
					DetectorType: detectorspb.DetectorType_Signalwire,
					Raw:          []byte(resMatch),
				}

				if verify {
					data := fmt.Sprintf("%s:%s", resID, resMatch)
					sEnc := b64.StdEncoding.EncodeToString([]byte(data))
					req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://%s/api/laml/2010-04-01/Accounts", resURL), nil)
					if err != nil {
						continue
					}
					req.Header.Add("Content-Type", "application/json")
					req.Header.Add("Authorization", fmt.Sprintf("Basic %s", sEnc))
					res, err := client.Do(req)
					if err == nil {
						defer res.Body.Close()
						if res.StatusCode >= 200 && res.StatusCode < 300 {
							s1.Verified = true
						} else {
							//This function will check false positives for common test words, but also it will make sure the key appears 'random' enough to be a real key
							if detectors.IsKnownFalsePositive(resMatch, detectors.DefaultFalsePositives, true) {
								continue
							}
						}
					}
				}

				results = append(results, s1)
			}
		}
	}

	return detectors.CleanResults(results), nil
}
