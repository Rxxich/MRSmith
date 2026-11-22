package rulesets

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"mrsmith/internal/apperr"
	"mrsmith/internal/fetch"
	"mrsmith/internal/rules"
	"mrsmith/internal/storage"
	"mrsmith/internal/store"
)

const (
	SourceURL    = "url"
	SourceObject = "object"

	StatusPending = "pending"
	StatusOK      = "ok"
	StatusError   = "error"

	maxSources    = 50
	maxOverrides  = 20000
	maxEntryLen   = 512
	MinIntervalM  = 5
	MaxIntervalM  = 7 * 24 * 60
	eventsToKeep  = 2000
	sampleEntries = 20
)

type Source struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	URL          string            `json:"url,omitempty"`
	Bucket       string            `json:"bucket,omitempty"`
	Key          string            `json:"key,omitempty"`
	Format       rules.InputFormat `json:"format"`
	BareAsSuffix bool              `json:"bareAsSuffix"`
	State        SourceState       `json:"state"`
}

type SourceState struct {
	Status           string            `json:"status"`
	Error            string            `json:"error,omitempty"`
	ETag             string            `json:"etag,omitempty"`
	LastModified     string            `json:"lastModified,omitempty"`
	CheckedAt        time.Time         `json:"checkedAt,omitzero"`
	ChangedAt        time.Time         `json:"changedAt,omitzero"`
	SHA256           string            `json:"sha256,omitempty"`
	Size             int64             `json:"size"`
	Rules            int               `json:"rules"`
	Invalid          int               `json:"invalid"`
	InvalidSample    []string          `json:"invalidSample,omitempty"`
	DetectedFormat   rules.InputFormat `json:"detectedFormat,omitempty"`
	DetectedBehavior rules.Behavior    `json:"detectedBehavior,omitempty"`
	// CacheKey is where the last downloaded copy lives in the downloads bucket.
	CacheKey string `json:"cacheKey,omitempty"`
}

type OutputInfo struct {
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256,omitempty"`
	Verified bool   `json:"verified"`
	Error    string `json:"error,omitempty"`
	// Set when the file holds only part of a mixed set: an MRS file of a
	// classical set keeps its domains or its IP networks.
	Behavior   rules.Behavior `json:"behavior,omitempty"`
	Count      int            `json:"count,omitempty"`
	Dropped    map[string]int `json:"dropped,omitempty"`
	Translated int            `json:"translated,omitempty"`
}

type State struct {
	Status       string                            `json:"status"`
	Error        string                            `json:"error,omitempty"`
	LastRunAt    time.Time                         `json:"lastRunAt,omitzero"`
	LastChangeAt time.Time                         `json:"lastChangeAt,omitzero"`
	NextRunAt    time.Time                         `json:"nextRunAt,omitzero"`
	Behavior     rules.Behavior                    `json:"behavior,omitempty"`
	Count        int                               `json:"count"`
	Domains      int                               `json:"domains"`
	CIDRs        int                               `json:"cidrs"`
	Classical    int                               `json:"classical"`
	Skipped      int                               `json:"skipped"`
	Dropped      map[string]int                    `json:"dropped,omitempty"`
	Optimized    int                               `json:"optimized"`
	Still        []rules.StillMatched              `json:"still,omitempty"`
	Outputs      map[rules.OutputFormat]OutputInfo `json:"outputs,omitempty"`
	Hash         string                            `json:"hash,omitempty"`
	// InputHash fingerprints what the last build was made from.
	InputHash  string `json:"inputHash,omitempty"`
	DurationMs int64  `json:"durationMs"`
}

type Ruleset struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Slug        string               `json:"slug"`
	Description string               `json:"description"`
	Behavior    rules.Behavior       `json:"behavior"`
	Formats     []rules.OutputFormat `json:"formats"`
	Sources     []Source             `json:"sources"`
	Include     []string             `json:"include"`
	Exclude     []rules.Exclusion    `json:"exclude"`
	Optimize    bool                 `json:"optimize"`
	AggregateIP bool                 `json:"aggregateIP"`
	NoResolve   bool                 `json:"noResolve"`
	IntervalMin int                  `json:"intervalMin"`
	Private     bool                 `json:"private"`
	// Notify sends Telegram notices about this set's changes. Off by default.
	Notify    bool      `json:"notify"`
	Token     string    `json:"token,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	State     State     `json:"state"`
	// Downloads counts fetches of the published files by format.
	Downloads map[rules.OutputFormat]store.Counter `json:"downloads,omitempty"`
}

type Event struct {
	ID            string    `json:"id"`
	Time          time.Time `json:"time"`
	RulesetID     string    `json:"rulesetId"`
	RulesetName   string    `json:"rulesetName"`
	Kind          string    `json:"kind"` // changed | unchanged | error
	Trigger       string    `json:"trigger"`
	Added         int       `json:"added"`
	Removed       int       `json:"removed"`
	Total         int       `json:"total"`
	SampleAdded   []string  `json:"sampleAdded,omitempty"`
	SampleRemoved []string  `json:"sampleRemoved,omitempty"`
	Message       string    `json:"message,omitempty"`
}

// Input is what a user may set on a rule set.
type Input struct {
	Name        string               `json:"name"`
	Slug        string               `json:"slug"`
	Description string               `json:"description"`
	Behavior    rules.Behavior       `json:"behavior"`
	Formats     []rules.OutputFormat `json:"formats"`
	Sources     []SourceInput        `json:"sources"`
	Include     []string             `json:"include"`
	Exclude     []rules.Exclusion    `json:"exclude"`
	Optimize    bool                 `json:"optimize"`
	AggregateIP bool                 `json:"aggregateIP"`
	NoResolve   bool                 `json:"noResolve"`
	IntervalMin int                  `json:"intervalMin"`
	Private     bool                 `json:"private"`
	Notify      bool                 `json:"notify"`
}

type SourceInput struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	URL          string            `json:"url"`
	Bucket       string            `json:"bucket"`
	Key          string            `json:"key"`
	Format       rules.InputFormat `json:"format"`
	BareAsSuffix bool              `json:"bareAsSuffix"`
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

func invalid(format string, a ...any) error { return apperr.Bad(format, a...) }

func (in *Input) normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if n := utf8.RuneCountInString(in.Name); n == 0 || n > 80 {
		return invalid("название: от 1 до 80 символов")
	}
	if utf8.RuneCountInString(in.Description) > 500 {
		return invalid("описание: не больше 500 символов")
	}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if in.Slug == "" {
		in.Slug = Slugify(in.Name)
	}
	if !slugRe.MatchString(in.Slug) {
		return invalid("имя файла в ссылке: латиница, цифры, точка, дефис и подчёркивание, до 63 символов")
	}
	if in.Behavior == "" {
		in.Behavior = rules.BehaviorAuto
	}
	if !in.Behavior.Valid() {
		return invalid("неизвестный behavior %q", in.Behavior)
	}
	if len(in.Formats) == 0 {
		return invalid("выберите хотя бы один выходной формат")
	}
	var formats []rules.OutputFormat
	for _, f := range rules.AllOutputs {
		if slices.Contains(in.Formats, f) {
			formats = append(formats, f)
		}
	}
	for _, f := range in.Formats {
		if !f.Valid() {
			return invalid("неизвестный формат %q", f)
		}
	}
	in.Formats = formats
	if in.IntervalMin != 0 && (in.IntervalMin < MinIntervalM || in.IntervalMin > MaxIntervalM) {
		return invalid("интервал обновления: от %d минут до 7 дней, или 0 — только вручную", MinIntervalM)
	}
	if len(in.Sources) > maxSources {
		return invalid("не больше %d источников", maxSources)
	}
	for i := range in.Sources {
		s := &in.Sources[i]
		if s.Format == "" {
			s.Format = rules.InputAuto
		}
		if !s.Format.Valid() {
			return invalid("источник %d: неизвестный формат %q", i+1, s.Format)
		}
		switch s.Type {
		case SourceURL:
			s.URL = strings.TrimSpace(s.URL)
			if err := fetch.ValidateURL(s.URL); err != nil {
				return invalid("источник %d: %v", i+1, err)
			}
			s.Bucket, s.Key = "", ""
		case SourceObject:
			if err := storage.ValidBucketName(s.Bucket); err != nil {
				return invalid("источник %d: %v", i+1, err)
			}
			k, err := storage.CleanKey(s.Key)
			if err != nil {
				return invalid("источник %d: %v", i+1, err)
			}
			s.Key, s.URL = k, ""
		default:
			return invalid("источник %d: тип должен быть url или object", i+1)
		}
		if s.ID != "" && !store.IsID(s.ID) {
			s.ID = ""
		}
	}
	var err error
	if in.Include, err = cleanOverrides(in.Include, "добавленные"); err != nil {
		return err
	}
	if in.Exclude, err = CleanExclusions(in.Exclude); err != nil {
		return err
	}
	if len(in.Sources) == 0 && len(in.Include) == 0 {
		return invalid("добавьте источник или хотя бы одно правило вручную")
	}
	return nil
}

// CleanExclusions normalizes exclusion rules and modes. A rule given twice
// keeps its last mode. A missing mode means "only this rule".
func CleanExclusions(list []rules.Exclusion) ([]rules.Exclusion, error) {
	if len(list) > maxOverrides {
		return nil, invalid("исключения: не больше %d", maxOverrides)
	}
	out := []rules.Exclusion{}
	pos := map[string]int{}
	for _, e := range list {
		if len(e.Rule) > maxEntryLen {
			return nil, invalid("исключения: слишком длинная строка")
		}
		if e.Mode == "" {
			e.Mode = rules.ExcludeExact
		}
		if !e.Mode.Valid() {
			return nil, invalid("исключения: неизвестный режим %q", e.Mode)
		}
		norm, bad := rules.NormalizeEntries([]string{e.Rule})
		if len(bad) > 0 {
			return nil, invalid("исключения: не удалось разобрать %s", bad[0])
		}
		for _, r := range norm {
			if i, ok := pos[r]; ok {
				out[i].Mode = e.Mode
				continue
			}
			pos[r] = len(out)
			out = append(out, rules.Exclusion{Rule: r, Mode: e.Mode})
		}
	}
	return out, nil
}

func cleanOverrides(list []string, what string) ([]string, error) {
	if len(list) > maxOverrides {
		return nil, invalid("%s правила: не больше %d", what, maxOverrides)
	}
	for _, e := range list {
		if len(e) > maxEntryLen {
			return nil, invalid("%s правила: слишком длинная строка", what)
		}
	}
	ok, bad := rules.NormalizeEntries(list)
	if len(bad) > 0 {
		sample := bad
		if len(sample) > 5 {
			sample = sample[:5]
		}
		return nil, invalid("%s правила: не удалось разобрать %s", what, strings.Join(sample, ", "))
	}
	if ok == nil {
		ok = []string{}
	}
	return ok, nil
}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i",
	'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t",
	'у': "u", 'ф': "f", 'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "",
	'э': "e", 'ю': "yu", 'я': "ya",
}

// Slugify derives a URL-safe file name from a title, transliterating Russian.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case translit[r] != "" || r == 'ъ' || r == 'ь':
			b.WriteString(translit[r])
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		s = "rules-" + store.RandomHex(3)
	}
	return s
}

var ErrNotFound = apperr.NotFound("набор правил не найден")
