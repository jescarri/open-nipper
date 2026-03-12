package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	ecBaseURL        = "https://api.weather.gc.ca"
	ecCollection     = "citypageweather-realtime"
	weatherTimeout   = 15 * time.Second
	bboxDelta        = 0.15 // ~17 km bounding box radius
	weatherUserAgent = "Open-Nipper-Agent/1.0 (+https://github.com/jescarri/open-nipper)"
)

// profileCoordsKey is the context key for fallback lat/lon from the user's profile.
type profileCoordsKey struct{}

// ContextWithProfileCoords stores the user's profile coordinates in ctx so ExecWeather
// can use them when the LLM calls get_weather without passing latitude/longitude.
func ContextWithProfileCoords(ctx context.Context, lat, lon float64) context.Context {
	return context.WithValue(ctx, profileCoordsKey{}, [2]float64{lat, lon})
}

// ProfileCoordsFromContext returns the profile coordinates from ctx, if set.
func ProfileCoordsFromContext(ctx context.Context) (lat, lon float64, ok bool) {
	v := ctx.Value(profileCoordsKey{})
	if v == nil {
		return 0, 0, false
	}
	arr, ok := v.([2]float64)
	if !ok || len(arr) != 2 {
		return 0, 0, false
	}
	return arr[0], arr[1], true
}

// WeatherParams defines the input schema for the weather tool.
type WeatherParams struct {
	Latitude  float64 `json:"latitude"  jsonschema:"description=Latitude of the location (decimal degrees). Use the user profile coordinates if available.,required"`
	Longitude float64 `json:"longitude" jsonschema:"description=Longitude of the location (decimal degrees). Use the user profile coordinates if available.,required"`
	Language  string  `json:"language"  jsonschema:"description=Language for the forecast: 'en' for English or 'fr' for French. Defaults to 'en'.,enum=en,enum=fr"`
}

// WeatherResult is the output of the weather tool.
type WeatherResult struct {
	City             string `json:"city"`
	Region           string `json:"region"`
	CurrentCondition string `json:"current_condition"`
	Temperature      string `json:"temperature"`
	WindChill        string `json:"wind_chill,omitempty"`
	Wind             string `json:"wind"`
	Humidity         string `json:"humidity"`
	Pressure         string `json:"pressure"`
	Forecast         string `json:"forecast"`
	Warnings         string `json:"warnings,omitempty"`
	ObservedAt       string `json:"observed_at"`
	StationName      string `json:"station_name"`
	PageURL          string `json:"page_url"`
}

// ExecWeather fetches weather data from the Environment Canada API.
// If latitude/longitude are zero, falls back to profile coordinates from context (set by the agent runtime).
func ExecWeather(ctx context.Context, params WeatherParams) (*WeatherResult, error) {
	if params.Latitude == 0 && params.Longitude == 0 {
		if lat, lon, ok := ProfileCoordsFromContext(ctx); ok {
			params.Latitude = lat
			params.Longitude = lon
		} else {
			return nil, fmt.Errorf("latitude and longitude are required — ask the user to set their coordinates via /setup coords <lat,lon>")
		}
	}

	lang := params.Language
	if lang == "" {
		lang = "en"
	}
	if lang != "en" && lang != "fr" {
		lang = "en"
	}

	bbox := fmt.Sprintf("%.4f,%.4f,%.4f,%.4f",
		params.Longitude-bboxDelta, params.Latitude-bboxDelta,
		params.Longitude+bboxDelta, params.Latitude+bboxDelta,
	)

	url := fmt.Sprintf("%s/collections/%s/items?f=json&limit=1&bbox=%s",
		ecBaseURL, ecCollection, bbox)

	client := &http.Client{Timeout: weatherTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building weather request: %w", err)
	}
	req.Header.Set("User-Agent", weatherUserAgent)
	req.Header.Set("Accept", "application/geo+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching weather data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("weather API returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, fmt.Errorf("reading weather response: %w", err)
	}

	return parseWeatherResponse(body, lang)
}

// --- JSON structures for the EC API response ---

type ecFeatureCollection struct {
	Features []ecFeature `json:"features"`
}

type ecFeature struct {
	Properties ecProperties `json:"properties"`
}

type ecProperties struct {
	Name              ecLangStr          `json:"name"`
	Region            ecLangStr          `json:"region"`
	CurrentConditions *ecCurrentCond     `json:"currentConditions"`
	ForecastGroup     *ecForecastGroup   `json:"forecastGroup"`
	Warnings          json.RawMessage    `json:"warnings"`
	URL               ecLangStr          `json:"url"`
}

type ecLangStr struct {
	En string `json:"en"`
	Fr string `json:"fr"`
}

func (l ecLangStr) get(lang string) string {
	if lang == "fr" && l.Fr != "" {
		return l.Fr
	}
	return l.En
}

type ecCurrentCond struct {
	Temperature  ecValueField `json:"temperature"`
	Condition    ecLangStr    `json:"condition"`
	Wind         ecWind       `json:"wind"`
	WindChill    ecValueField `json:"windChill"`
	Humidity     ecValueField `json:"relativeHumidity"`
	Pressure     ecPressure   `json:"pressure"`
	Timestamp    ecLangStr    `json:"timestamp"`
	Station      ecStation    `json:"station"`
}

type ecValueField struct {
	Value ecLangVal   `json:"value"`
	Units ecLangStr   `json:"units"`
}

type ecLangVal struct {
	En json.RawMessage `json:"en"`
	Fr json.RawMessage `json:"fr"`
}

func (v ecLangVal) get(lang string) string {
	raw := v.En
	if lang == "fr" && len(v.Fr) > 0 {
		raw = v.Fr
	}
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		if f == float64(int64(f)) {
			return strconv.FormatInt(int64(f), 10)
		}
		return strconv.FormatFloat(f, 'f', 1, 64)
	}
	var i int64
	if err := json.Unmarshal(raw, &i); err == nil {
		return strconv.FormatInt(i, 10)
	}
	return string(raw)
}

type ecWind struct {
	Speed     ecValueField `json:"speed"`
	Gust      ecValueField `json:"gust"`
	Direction ecLangStr    `json:"direction"`
}

type ecPressure struct {
	Value    ecLangVal `json:"value"`
	Units    ecLangStr `json:"units"`
	Tendency ecLangStr `json:"tendency"`
}

type ecStation struct {
	Value ecLangStr `json:"value"`
}

type ecForecastGroup struct {
	Forecasts []ecForecast `json:"forecasts"`
}

type ecForecast struct {
	Period      ecPeriod  `json:"period"`
	TextSummary ecLangStr `json:"textSummary"`
}

type ecPeriod struct {
	TextForecastName ecLangStr `json:"textForecastName"`
}

func parseWeatherResponse(data []byte, lang string) (*WeatherResult, error) {
	var fc ecFeatureCollection
	if err := json.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("decoding weather response: %w", err)
	}

	if len(fc.Features) == 0 {
		return nil, fmt.Errorf("no weather data found for the given coordinates — the location may not be in Canada or near an Environment Canada station")
	}

	props := fc.Features[0].Properties
	result := &WeatherResult{
		City:   props.Name.get(lang),
		Region: props.Region.get(lang),
	}

	if props.URL.En != "" {
		result.PageURL = props.URL.get(lang)
	}

	if cc := props.CurrentConditions; cc != nil {
		result.CurrentCondition = cc.Condition.get(lang)
		result.ObservedAt = cc.Timestamp.get(lang)
		result.StationName = cc.Station.Value.get(lang)

		tempVal := cc.Temperature.Value.get(lang)
		tempUnit := cc.Temperature.Units.get(lang)
		if tempVal != "" {
			result.Temperature = fmt.Sprintf("%s°%s", tempVal, tempUnit)
		}

		wcVal := cc.WindChill.Value.get(lang)
		if wcVal != "" {
			result.WindChill = fmt.Sprintf("%s°C", wcVal)
		}

		windSpeed := cc.Wind.Speed.Value.get(lang)
		windUnit := cc.Wind.Speed.Units.get(lang)
		windDir := cc.Wind.Direction.get(lang)
		gustVal := cc.Wind.Gust.Value.get(lang)
		if windSpeed != "" {
			result.Wind = fmt.Sprintf("%s %s %s", windDir, windSpeed, windUnit)
			if gustVal != "" && gustVal != "0" {
				result.Wind += fmt.Sprintf(" (gusting %s %s)", gustVal, windUnit)
			}
		}

		humVal := cc.Humidity.Value.get(lang)
		humUnit := cc.Humidity.Units.get(lang)
		if humVal != "" {
			result.Humidity = fmt.Sprintf("%s%s", humVal, humUnit)
		}

		pressVal := cc.Pressure.Value.get(lang)
		pressUnit := cc.Pressure.Units.get(lang)
		pressTend := cc.Pressure.Tendency.get(lang)
		if pressVal != "" {
			result.Pressure = fmt.Sprintf("%s %s", pressVal, pressUnit)
			if pressTend != "" {
				result.Pressure += fmt.Sprintf(" (%s)", pressTend)
			}
		}
	}

	if fg := props.ForecastGroup; fg != nil {
		var forecasts []string
		maxForecasts := 14 // 7 days × day/night periods
		if len(fg.Forecasts) < maxForecasts {
			maxForecasts = len(fg.Forecasts)
		}
		for _, f := range fg.Forecasts[:maxForecasts] {
			period := f.Period.TextForecastName.get(lang)
			summary := f.TextSummary.get(lang)
			if period != "" && summary != "" {
				forecasts = append(forecasts, fmt.Sprintf("*%s*: %s", period, summary))
			}
		}
		result.Forecast = strings.Join(forecasts, "\n")
	}

	if len(props.Warnings) > 0 {
		var warnings []interface{}
		if err := json.Unmarshal(props.Warnings, &warnings); err == nil && len(warnings) > 0 {
			result.Warnings = fmt.Sprintf("%d active warning(s) — check %s for details", len(warnings), result.PageURL)
		}
	}

	return result, nil
}
