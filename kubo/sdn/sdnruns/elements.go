package sdnruns

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Element download formats.
const (
	FormatTLE = "tle"
	FormatOMM = "omm"
	FormatCDM = "cdm"
)

// RenderElements renders one object's fitted element set in the requested format:
//
//	tle -> classic NORAD two-line element set (with name line + checksums)
//	omm -> CCSDS OMM keyword-value (KVN) message
//	cdm -> Space Force VCM (Vector Covariance Message) element block
//
// All three carry the same mean elements; "cdm"/VCM is the Space Force element
// message form the board offers for download. Returns the text, a content type,
// a suggested filename, and false for an unknown format.
func RenderElements(obj ObjectResult, format string) (body, contentType, filename string, ok bool) {
	el := obj.Elements
	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatTLE, "":
		return renderTLE(el), "text/plain; charset=utf-8", fmt.Sprintf("%d.tle", obj.Norad), true
	case FormatOMM:
		return renderOMMKVN(el), "text/plain; charset=utf-8", fmt.Sprintf("%d.omm", obj.Norad), true
	case FormatCDM:
		return renderVCM(obj), "text/plain; charset=utf-8", fmt.Sprintf("%d.vcm", obj.Norad), true
	default:
		return "", "", "", false
	}
}

// renderTLE builds a classic NORAD two-line element set from the mean elements.
func renderTLE(el Elements) string {
	yy, ddd := tleEpoch(el.Epoch)
	intl := tleIntlDesignator(el.ObjectID)
	norad := el.NoradCatID
	classification := "U"
	if el.Classification != "" {
		classification = string(el.Classification[0])
	}
	elset := el.ElementSetNo
	if elset == 0 {
		elset = 999
	}

	// Line 1: catalog id, classification, intl designator, epoch, ndot/2, ndotdot,
	// bstar, ephemeris type, element set number.
	line1 := fmt.Sprintf("1 %05d%s %-8s %02d%012.8f %s %s %s 0 %4d",
		norad, classification, intl, yy, ddd,
		tleFirstDeriv(el.MeanMotionDot),
		tleExpField(el.MeanMotionDdot),
		tleExpField(el.Bstar),
		elset%10000,
	)
	line1 = fmt.Sprintf("%s%d", line1, tleChecksum(line1))

	// Line 2: inclination, RAAN, eccentricity (7-digit assumed-decimal), arg
	// perigee, mean anomaly, mean motion, rev number at epoch.
	line2 := fmt.Sprintf("2 %05d %8.4f %8.4f %07d %8.4f %8.4f %11.8f%5d",
		norad,
		el.Inclination,
		wrap360(el.RaOfAscNode),
		eccDigits(el.Eccentricity),
		wrap360(el.ArgOfPericenter),
		wrap360(el.MeanAnomaly),
		el.MeanMotion,
		int(math.Mod(el.RevAtEpoch, 100000)),
	)
	line2 = fmt.Sprintf("%s%d", line2, tleChecksum(line2))

	name := el.ObjectName
	if name == "" {
		name = fmt.Sprintf("NORAD %d", norad)
	}
	return fmt.Sprintf("%s\n%s\n%s\n", name, line1, line2)
}

// renderOMMKVN builds a CCSDS OMM keyword-value (KVN) message.
func renderOMMKVN(el Elements) string {
	var b strings.Builder
	now := time.Now().UTC().Format("2006-01-02T15:04:05")
	fmt.Fprintf(&b, "CCSDS_OMM_VERS = 3.0\n")
	fmt.Fprintf(&b, "CREATION_DATE = %s\n", now)
	fmt.Fprintf(&b, "ORIGINATOR = SDN-OD\n\n")
	fmt.Fprintf(&b, "META_START\n")
	fmt.Fprintf(&b, "OBJECT_NAME = %s\n", orNA(el.ObjectName))
	fmt.Fprintf(&b, "OBJECT_ID = %s\n", orNA(el.ObjectID))
	fmt.Fprintf(&b, "CENTER_NAME = EARTH\n")
	fmt.Fprintf(&b, "REF_FRAME = TEME\n")
	fmt.Fprintf(&b, "TIME_SYSTEM = UTC\n")
	fmt.Fprintf(&b, "MEAN_ELEMENT_THEORY = SGP4\n")
	fmt.Fprintf(&b, "META_STOP\n\n")
	fmt.Fprintf(&b, "COMMENT Supplemental GP — OD fit of operator ephemeris (RMS %.3f km, %s)\n",
		el.RMSKm, convergedText(el.Converged))
	fmt.Fprintf(&b, "EPOCH = %s\n", orNA(el.Epoch))
	fmt.Fprintf(&b, "MEAN_MOTION = %.8f\n", el.MeanMotion)
	fmt.Fprintf(&b, "ECCENTRICITY = %.7f\n", el.Eccentricity)
	fmt.Fprintf(&b, "INCLINATION = %.4f\n", el.Inclination)
	fmt.Fprintf(&b, "RA_OF_ASC_NODE = %.4f\n", el.RaOfAscNode)
	fmt.Fprintf(&b, "ARG_OF_PERICENTER = %.4f\n", el.ArgOfPericenter)
	fmt.Fprintf(&b, "MEAN_ANOMALY = %.4f\n", el.MeanAnomaly)
	fmt.Fprintf(&b, "GM = 398600.4418\n\n")
	fmt.Fprintf(&b, "EPHEMERIS_TYPE = %d\n", el.EphemerisType)
	fmt.Fprintf(&b, "CLASSIFICATION_TYPE = %s\n", orDefault(el.Classification, "U"))
	fmt.Fprintf(&b, "NORAD_CAT_ID = %d\n", el.NoradCatID)
	fmt.Fprintf(&b, "ELEMENT_SET_NO = %d\n", el.ElementSetNo)
	fmt.Fprintf(&b, "REV_AT_EPOCH = %.0f\n", el.RevAtEpoch)
	fmt.Fprintf(&b, "BSTAR = %.8e\n", el.Bstar)
	fmt.Fprintf(&b, "MEAN_MOTION_DOT = %.8e\n", el.MeanMotionDot)
	fmt.Fprintf(&b, "MEAN_MOTION_DDOT = %.8e\n", el.MeanMotionDdot)
	return b.String()
}

// renderVCM builds a Space Force VCM (Vector Covariance Message) element block
// from the mean elements. The VCM is the legacy Space Force element message; this
// carries the epoch, the SGP4 mean Keplerian element set, and the drag term, with
// the covariance section marked not-estimated for a mean-element-only fit.
func renderVCM(obj ObjectResult) string {
	el := obj.Elements
	var b strings.Builder
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000")
	fmt.Fprintf(&b, "VCM\n")
	fmt.Fprintf(&b, "MESSAGE FORMAT     = SPACE FORCE VECTOR COVARIANCE MESSAGE\n")
	fmt.Fprintf(&b, "GENERATED AT (UTC) = %s\n", now)
	fmt.Fprintf(&b, "ORIGINATOR         = SDN-OD (supplemental GP)\n")
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "SATELLITE NAME     = %s\n", orNA(el.ObjectName))
	fmt.Fprintf(&b, "CATALOG ID         = %d\n", el.NoradCatID)
	fmt.Fprintf(&b, "INTERNATIONAL DESG = %s\n", orNA(el.ObjectID))
	fmt.Fprintf(&b, "EPOCH (UTC)        = %s\n", orNA(el.Epoch))
	fmt.Fprintf(&b, "REF FRAME          = TEME\n")
	fmt.Fprintf(&b, "TIME SYSTEM        = UTC\n")
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "MEAN ELEMENTS (SGP4):\n")
	fmt.Fprintf(&b, "  MEAN MOTION       = %.8f rev/day\n", el.MeanMotion)
	fmt.Fprintf(&b, "  ECCENTRICITY      = %.7f\n", el.Eccentricity)
	fmt.Fprintf(&b, "  INCLINATION       = %.4f deg\n", el.Inclination)
	fmt.Fprintf(&b, "  RA OF ASC NODE    = %.4f deg\n", el.RaOfAscNode)
	fmt.Fprintf(&b, "  ARG OF PERIGEE    = %.4f deg\n", el.ArgOfPericenter)
	fmt.Fprintf(&b, "  MEAN ANOMALY      = %.4f deg\n", el.MeanAnomaly)
	fmt.Fprintf(&b, "  MEAN MOTION DOT   = %.8e rev/day^2\n", el.MeanMotionDot)
	fmt.Fprintf(&b, "  MEAN MOTION DDOT  = %.8e rev/day^3\n", el.MeanMotionDdot)
	fmt.Fprintf(&b, "  BSTAR             = %.8e 1/ER\n", el.Bstar)
	fmt.Fprintf(&b, "  EPHEMERIS TYPE    = %d\n", el.EphemerisType)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "FIT QUALITY:\n")
	fmt.Fprintf(&b, "  RMS               = %.3f km\n", el.RMSKm)
	fmt.Fprintf(&b, "  CONVERGED         = %s\n", convergedText(el.Converged))
	if obj.CelestrakRMS != nil {
		fmt.Fprintf(&b, "  CELESTRAK RMS     = %.3f km (same-ephemeris reference)\n", *obj.CelestrakRMS)
	}
	if obj.SpacetrackRMS != nil {
		fmt.Fprintf(&b, "  SPACE-TRACK RMS   = %.3f km (same-ephemeris reference)\n", *obj.SpacetrackRMS)
	}
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "COVARIANCE         = NOT ESTIMATED (mean-element fit)\n")
	return b.String()
}

// ---- helpers ----

// tleEpoch converts an ISO epoch to the TLE two-digit-year + fractional-day-of-
// year form.
func tleEpoch(epoch string) (yy int, ddd float64) {
	t, err := time.Parse(time.RFC3339, normalizeEpoch(epoch))
	if err != nil {
		// Best-effort: try without a zone or fractional seconds.
		for _, layout := range []string{"2006-01-02T15:04:05.999999Z", "2006-01-02T15:04:05Z", "2006-01-02T15:04:05"} {
			if pt, e := time.Parse(layout, strings.TrimSuffix(epoch, "Z")+"Z"); e == nil {
				t = pt
				err = nil
				break
			}
		}
		if err != nil {
			return 0, 0
		}
	}
	t = t.UTC()
	yy = t.Year() % 100
	dayOfYear := float64(t.YearDay())
	frac := (float64(t.Hour())*3600 + float64(t.Minute())*60 + float64(t.Second()) + float64(t.Nanosecond())/1e9) / 86400.0
	return yy, dayOfYear + frac
}

// tleIntlDesignator maps an OMM OBJECT_ID (e.g. "1998-067-A") to the TLE launch
// designator (YYLLLP, e.g. "98067A"). Returns "" on an unparseable id.
func tleIntlDesignator(objectID string) string {
	s := strings.ReplaceAll(strings.TrimSpace(objectID), "-", "")
	if len(s) < 5 {
		return s
	}
	// s like "1998067A": drop the century digits.
	if len(s) >= 6 && s[0] == '1' || len(s) >= 6 && s[0] == '2' {
		if len(s) >= 8 {
			return s[2:] // 1998067A -> 98067A
		}
	}
	return s
}

// tleFirstDeriv formats the mean-motion first derivative for TLE line 1
// (leading-sign, assumed-decimal 8 digits, e.g. " .00001234" / "-.00001234").
func tleFirstDeriv(v float64) string {
	sign := " "
	if v < 0 {
		sign = "-"
	}
	a := math.Abs(v)
	// TLE stores this field as .NNNNNNNN (fractional). Clamp to the field width.
	digits := int(math.Round(a * 1e8))
	if digits > 99999999 {
		digits = 99999999
	}
	return fmt.Sprintf("%s.%08d", sign, digits)
}

// tleExpField formats a value in the TLE assumed-decimal exponential form
// (±MMMMM±E meaning .MMMMM×10^E), used for ndotdot and bstar.
func tleExpField(v float64) string {
	if v == 0 {
		return " 00000-0"
	}
	sign := "+"
	if v < 0 {
		sign = "-"
	}
	a := math.Abs(v)
	exp := 0
	for a >= 1 {
		a /= 10
		exp++
	}
	for a < 0.1 {
		a *= 10
		exp--
	}
	mant := int(math.Round(a * 1e5))
	if mant > 99999 {
		mant = 99999
	}
	expSign := "+"
	if exp < 0 {
		expSign = "-"
	}
	e := int(math.Abs(float64(exp)))
	if e > 9 {
		e = 9
	}
	return fmt.Sprintf("%s%05d%s%d", sign, mant, expSign, e)
}

// eccDigits returns the 7 TLE eccentricity digits (assumed leading decimal): e.g.
// 0.0006726 -> 6726 -> "0006726".
func eccDigits(e float64) int {
	d := int(math.Round(math.Abs(e) * 1e7))
	if d > 9999999 {
		d = 9999999
	}
	return d
}

// tleChecksum is the TLE mod-10 checksum: sum of digits, with each '-' counting 1.
func tleChecksum(line string) int {
	sum := 0
	for _, c := range line {
		switch {
		case c >= '0' && c <= '9':
			sum += int(c - '0')
		case c == '-':
			sum++
		}
	}
	return sum % 10
}

func wrap360(v float64) float64 {
	m := math.Mod(v, 360)
	if m < 0 {
		m += 360
	}
	return m
}

func orNA(s string) string {
	if strings.TrimSpace(s) == "" {
		return "N/A"
	}
	return s
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func convergedText(c bool) string {
	if c {
		return "converged"
	}
	return "not converged"
}
