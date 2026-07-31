package cli

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"gozillo/internal/output"
	"gozillo/internal/zillow"
)

type propertyCommand struct{}

func (propertyCommand) Name() string    { return "property" }
func (propertyCommand) Summary() string { return "Extract normalized Zillow property details" }

func (propertyCommand) Run(ctx Context, args []string) error {
	if wantsHelp(args) {
		writePropertyUsage(ctx.Stdout)
		return nil
	}

	flags := flag.NewFlagSet("property", flag.ContinueOnError)
	flags.SetOutput(ctx.Stderr)
	includeRaw := flags.Bool("raw", false, "include the raw property object in JSON output")
	timeout := flags.Duration("timeout", zillow.DefaultTimeout, "HTTP request timeout for URL input")
	proxyValue := flags.String("proxy", "", "HTTP/HTTPS/SOCKS5 proxy URL for URL input")
	sessionName := flags.String("session", "", "named browser-derived session for URL input")
	tlsProfile := flags.String("tls-profile", "", "required tls-client browser profile for URL input")
	userAgent := flags.String("user-agent", "", "explicit User-Agent for URL input")
	var browserHeaderValues stringListFlag
	flags.Var(&browserHeaderValues, "browser-header", "allowlisted browser HTML header as Name: Value; repeat as needed")
	if err := flags.Parse(args); err != nil {
		return &usageError{err: err}
	}
	if flags.NArg() != 1 {
		return usagef("property requires exactly one Zillow URL or local HTML file")
	}
	browserHeaders, err := parseBrowserHeaders(browserHeaderValues)
	if err != nil {
		return usagef("property --browser-header: %v", err)
	}
	if *timeout <= 0 {
		return usagef("property --timeout must be positive")
	}

	source := flags.Arg(0)
	var property *zillow.Property
	if isHTTPURL(source) {
		if strings.TrimSpace(*tlsProfile) == "" {
			return usagef("property --tls-profile is required for URL input")
		}
		client, clientErr := newZillowTransport(currentVersion(), zillowTransportOptions{
			Timeout:        *timeout,
			ProxyURL:       *proxyValue,
			SessionName:    *sessionName,
			TLSProfile:     *tlsProfile,
			UserAgent:      *userAgent,
			BrowserHeaders: browserHeaders,
		})
		if clientErr != nil {
			return clientErr
		}
		property, err = client.FetchPropertyWithOptions(context.Background(), source, zillow.PropertyOptions{IncludeRaw: *includeRaw})
	} else {
		if networkOptionsSet(*proxyValue, *sessionName, *tlsProfile, *userAgent) || len(browserHeaders) > 0 {
			return usagef("property --proxy, --session, --tls-profile, --user-agent, and --browser-header are only valid for URL input")
		}
		file, openErr := os.Open(source)
		if openErr != nil {
			return fmt.Errorf("open property HTML: %w", openErr)
		}
		defer file.Close()
		property, err = zillow.ReadPropertyWithOptions(file, zillow.PropertyReaderOptions{IncludeRaw: *includeRaw})
	}
	if err != nil {
		return explainZillowError(err)
	}

	printer, err := output.NewPrinter(ctx.Stdout, ctx.OutputMode)
	if err != nil {
		return err
	}
	if ctx.OutputMode == output.ModeTable {
		return printer.Print(propertyTable(property))
	}
	return printer.Print(property)
}

func writePropertyUsage(w interface{ Write([]byte) (int, error) }) {
	_, _ = w.Write([]byte(`Usage:
  gozillo [global options] property [options] <Zillow URL | HTML file>

Options:
  --raw                  Include the raw property JSON in JSON output
  --timeout <duration>   HTTP timeout for URL input (default 20s)
  --proxy <URL>          HTTP/HTTPS/SOCKS5 proxy for URL input
  --session <name>       Imported HAR session for URL input
  --tls-profile <name>   Required browser profile for URL input
  --user-agent <value>   Explicit User-Agent; default is gozillo/<version>
  --browser-header <h:v> Allowlisted browser HTML header; repeat as needed
`))
}

func propertyTable(property *zillow.Property) output.Table {
	return output.Table{
		Headers: []string{"FIELD", "VALUE"},
		Rows: [][]string{
			{"ZPID", property.ID},
			{"Address", formatAddress(property.Address)},
			{"Base rent", formatMoney(property.Price, "")},
			{"Required monthly fees", formatMoney(property.RequiredMonthlyFees, "")},
			{"Total monthly cost", formatMoney(property.TotalMonthlyCost, "")},
			{"Beds", formatFloat(property.Bedrooms)},
			{"Baths", formatFloat(property.Bathrooms)},
			{"Living area", formatInteger(property.LivingArea)},
			{"Year built", formatPlainInteger(property.YearBuilt)},
			{"Available", property.Availability},
			{"Days on Zillow", formatInteger(property.DaysOnZillow)},
			{"Listed date", property.ListedDate},
			{"Updated date", property.UpdatedDate},
			{"Laundry", property.Laundry},
			{"Laundry features", strings.Join(property.LaundryFeatures, ", ")},
			{"Parking", property.Parking},
			{"Parking features", strings.Join(property.ParkingFeatures, ", ")},
			{"Pet policy", property.PetPolicy},
			{"Allowed pets", strings.Join(property.AllowedPets, ", ")},
			{"Flex spaces", strings.Join(property.FlexSpaces, ", ")},
			{"Verification", strings.Join(property.VerificationNotes, "; ")},
			{"Home type", property.HomeType},
			{"Status", property.Status},
			{"URL", property.URL},
		},
	}
}

func isHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
