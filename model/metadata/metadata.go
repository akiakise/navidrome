package metadata

import (
	"cmp"
	"io/fs"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/utils/slice"
)

type Info struct {
	FileInfo        FileInfo
	Tags            map[string][]string
	AudioProperties AudioProperties
	HasPicture      bool
}

type FileInfo interface {
	fs.FileInfo
	BirthTime() time.Time
}

type AudioProperties struct {
	Duration   time.Duration
	BitRate    int
	BitDepth   int
	SampleRate int
	Channels   int
}

type Date string

func (d Date) Year() int {
	if d == "" {
		return 0
	}
	y, _ := strconv.Atoi(string(d[:4]))
	return y
}

type Pair string

func (p Pair) Key() string   { return p.parse(0) }
func (p Pair) Value() string { return p.parse(1) }
func (p Pair) parse(i int) string {
	parts := strings.SplitN(string(p), consts.Zwsp, 2)
	if len(parts) > i {
		return parts[i]
	}
	return ""
}
func (p Pair) String() string {
	return string(p)
}
func NewPair(key, value string) string {
	return key + consts.Zwsp + value
}

func New(filePath string, info Info) Metadata {
	return Metadata{
		filePath:   filePath,
		fileInfo:   info.FileInfo,
		tags:       clean(filePath, info.Tags),
		audioProps: info.AudioProperties,
		hasPicture: info.HasPicture,
	}
}

type Metadata struct {
	filePath   string
	fileInfo   FileInfo
	tags       model.Tags
	audioProps AudioProperties
	hasPicture bool
}

func (md Metadata) FilePath() string     { return md.filePath }
func (md Metadata) ModTime() time.Time   { return md.fileInfo.ModTime() }
func (md Metadata) BirthTime() time.Time { return md.fileInfo.BirthTime() }
func (md Metadata) Size() int64          { return md.fileInfo.Size() }
func (md Metadata) Suffix() string {
	return strings.ToLower(strings.TrimPrefix(path.Ext(md.filePath), "."))
}
func (md Metadata) AudioProperties() AudioProperties         { return md.audioProps }
func (md Metadata) Length() float32                          { return float32(md.audioProps.Duration.Milliseconds()) / 1000 }
func (md Metadata) HasPicture() bool                         { return md.hasPicture }
func (md Metadata) All() model.Tags                          { return md.tags }
func (md Metadata) Strings(key model.TagName) []string       { return md.tags[key] }
func (md Metadata) String(key model.TagName) string          { return md.first(key) }
func (md Metadata) Int(key model.TagName) int64              { v, _ := strconv.Atoi(md.first(key)); return int64(v) }
func (md Metadata) Bool(key model.TagName) bool              { v, _ := strconv.ParseBool(md.first(key)); return v }
func (md Metadata) Date(key model.TagName) Date              { return md.date(key) }
func (md Metadata) NumAndTotal(key model.TagName) (int, int) { return md.tuple(key) }
func (md Metadata) Float(key model.TagName, def ...float64) float64 {
	return float(md.first(key), def...)
}
func (md Metadata) Gain(key model.TagName) float64 {
	v := strings.TrimSpace(strings.Replace(md.first(key), "dB", "", 1))
	return float(v)
}
func (md Metadata) Pairs(key model.TagName) []Pair {
	values := md.tags[key]
	return slice.Map(values, func(v string) Pair { return Pair(v) })
}
func (md Metadata) first(key model.TagName) string {
	if v, ok := md.tags[key]; ok && len(v) > 0 {
		return v[0]
	}
	return ""
}

func float(value string, def ...float64) float64 {
	v, err := strconv.ParseFloat(value, 64)
	if err != nil || v == math.Inf(-1) || v == math.Inf(1) {
		if len(def) > 0 {
			return def[0]
		}
		return 0
	}
	return v
}

// Used for tracks and discs
func (md Metadata) tuple(key model.TagName) (int, int) {
	tag := md.first(key)
	if tag == "" {
		return 0, 0
	}
	tuple := strings.Split(tag, "/")
	t1, t2 := 0, 0
	t1, _ = strconv.Atoi(tuple[0])
	if len(tuple) > 1 {
		t2, _ = strconv.Atoi(tuple[1])
	} else {
		t2tag := md.first(key + "total")
		t2, _ = strconv.Atoi(t2tag)
	}
	return t1, t2
}

var dateRegex = regexp.MustCompile(`([12]\d\d\d)`)

func (md Metadata) date(tagName model.TagName) Date {
	return Date(md.first(tagName))
}

// date tries to parse a date from a tag, it tries to get at least the year. See the tests for examples.
func parseDate(filePath string, tagName model.TagName, tagValue string) string {
	if len(tagValue) < 4 {
		return ""
	}

	// first get just the year
	match := dateRegex.FindStringSubmatch(tagValue)
	if len(match) == 0 {
		log.Warn("Error parsing date", "file", filePath, "tag", tagName, "date", tagValue)
		return ""
	}

	// if the tag is just the year, return it
	if len(tagValue) < 5 {
		return match[1]
	}

	// if the tag is too long, truncate it
	tagValue = tagValue[:min(10, len(tagValue))]

	// then try to parse the full date
	for _, mask := range []string{"2006-01-02", "2006-01"} {
		_, err := time.Parse(mask, tagValue)
		if err == nil {
			return tagValue
		}
	}
	log.Warn("Error parsing month and day from date", "file", filePath, "tag", tagName, "date", tagValue)
	return match[1]
}

// clean filters out tags that are not in the mappings or are empty,
// combine equivalent tags and remove duplicated values.
// It keeps the order of the tags names as they are defined in the mappings.
func clean(filePath string, tags map[string][]string) model.Tags {
	lowered := make(model.Tags, len(tags))
	for k, v := range tags {
		lowered[model.TagName(strings.ToLower(k))] = v
	}

	mappings := mappings()
	cleaned := make(model.Tags, len(mappings))
	for name, mapping := range mappings {
		for _, k := range mapping.Aliases {
			if mapping.Type == TagTypePair {
				prefix := name + ":"
				for tagKey, tagValues := range lowered {
					if strings.HasPrefix(string(tagKey), string(prefix)) {
						key := strings.TrimPrefix(string(tagKey), string(prefix))
						for _, value := range tagValues {
							cleaned[name] = append(cleaned[name], NewPair(key, value))
						}
					}
				}
			} else if v, ok := lowered[model.TagName(k)]; ok {
				v = split(v, mapping.Split)
				if len(v) > 0 {
					if existing, exists := cleaned[name]; exists {
						cleaned[name] = append(existing, v...)
					} else {
						cleaned[name] = v
					}
				}
			}
		}
	}

	for k, v := range cleaned {
		clean := removeDuplicatedAndEmpty(v)
		if len(clean) == 0 {
			delete(cleaned, k)
		} else {
			cleaned[k] = clean
		}
	}

	return sanitizeAll(filePath, cleaned)
}

// split a tag value by the given separators, but only if it has a single value.
func split(values []string, sep []string) []string {
	if len(values) != 1 || len(sep) == 0 {
		return values
	}
	tag := values[0]
	for _, s := range sep {
		re, err := regexp.Compile(regexp.QuoteMeta(s))
		if err != nil {
			log.Error("Error compiling regexp", "sep", s, err)
			continue
		}
		tag = re.ReplaceAllStringFunc(tag, func(_ string) string {
			return consts.Zwsp
		})
	}
	return strings.Split(tag, consts.Zwsp)
}

func removeDuplicatedAndEmpty(values []string) []string {
	seen := map[string]struct{}{}
	var result []string
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		result = append(result, v)
	}
	return result
}

func sanitizeAll(filePath string, tags model.Tags) model.Tags {
	cleaned := model.Tags{}
	for k, v := range tags {
		tag, found := mappings()[k]
		if !found {
			continue
		}

		var values []string
		for _, value := range v {
			cleanedValue := sanitize(filePath, k, tag, value)
			if cleanedValue != "" {
				values = append(values, cleanedValue)
			}
		}
		if len(values) > 0 {
			cleaned[k] = values
		}
	}
	return cleaned
}

const defaultMaxTagLength = 1024

func sanitize(filePath string, tagName model.TagName, tag tagConf, value string) string {
	// First truncate the value to the maximum length
	maxLength := cmp.Or(tag.MaxLength, defaultMaxTagLength)
	if len(value) > maxLength {
		log.Trace("Truncated tag value", "tag", tagName, "value", value, "length", len(value), "maxLength", maxLength)
		value = value[:maxLength]
	}

	switch tag.Type {
	case TagTypeDate:
		value = parseDate(filePath, tagName, value)
		if value == "" {
			log.Trace("Invalid date tag value", "tag", tagName, "value", value)
		}
	case TagTypeInteger:
		_, err := strconv.Atoi(value)
		if err != nil {
			log.Trace("Invalid integer tag value", "tag", tagName, "value", value)
			return ""
		}
	case TagTypeFloat:
		_, err := strconv.ParseFloat(value, 64)
		if err != nil {
			log.Trace("Invalid float tag value", "tag", tagName, "value", value)
			return ""
		}
	case TagTypeUUID:
		_, err := uuid.Parse(value)
		if err != nil {
			log.Trace("Invalid UUID tag value", "tag", tagName, "value", value)
			return ""
		}
	}
	return value
}
