package accounting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type participantsDocument struct {
	SchemaVersion int                 `json:"schema_version"`
	EpochIndex    uint64              `json:"epoch_index"`
	Participants  []ParticipantRecord `json:"participants"`
}

func fillNonZero(value reflect.Value, seed *int) {
	*seed++
	switch value.Kind() {
	case reflect.String:
		value.SetString(fmt.Sprintf("s%d<&> é", *seed))
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(int64(*seed))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(uint64(*seed))
	case reflect.Pointer:
		value.Set(reflect.New(value.Type().Elem()))
		fillNonZero(value.Elem(), seed)
	case reflect.Slice:
		value.Set(reflect.MakeSlice(value.Type(), 2, 2))
		for i := range 2 {
			fillNonZero(value.Index(i), seed)
		}
	case reflect.Map:
		value.Set(reflect.MakeMap(value.Type()))
		key := reflect.New(value.Type().Key()).Elem()
		fillNonZero(key, seed)
		element := reflect.New(value.Type().Elem()).Elem()
		fillNonZero(element, seed)
		value.SetMapIndex(key, element)
	case reflect.Struct:
		if value.Type() == reflect.TypeFor[time.Time]() {
			value.Set(reflect.ValueOf(time.Unix(int64(1700000000+*seed), 123456789).UTC()))
			return
		}
		for i := range value.NumField() {
			if field := value.Type().Field(i); field.IsExported() || field.Anonymous {
				fillNonZero(value.Field(i), seed)
			}
		}
	}
}

func fullRecords(count int) []ParticipantRecord {
	records := make([]ParticipantRecord, count)
	seed := 0
	for i := range records {
		fillNonZero(reflect.ValueOf(&records[i]).Elem(), &seed)
	}
	return records
}

func assertEveryLeafNonZero(t *testing.T, path string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			t.Errorf("%s is an empty object", path)
		}
		for key, child := range typed {
			assertEveryLeafNonZero(t, path+"."+key, child)
		}
	case []any:
		if len(typed) == 0 {
			t.Errorf("%s is an empty array", path)
		}
		for i, child := range typed {
			assertEveryLeafNonZero(t, fmt.Sprintf("%s[%d]", path, i), child)
		}
	case nil:
		t.Errorf("%s is null", path)
	case string:
		if typed == "" {
			t.Errorf("%s is empty", path)
		}
	case float64:
		if typed == 0 {
			t.Errorf("%s is zero", path)
		}
	case bool:
		if !typed {
			t.Errorf("%s is false", path)
		}
	}
}

type captured struct {
	status int
	header http.Header
	body   []byte
}

func fetch(t *testing.T, handler http.HandlerFunc) captured {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	response.Header.Del("Date")
	return captured{status: response.StatusCode, header: response.Header, body: body}
}

func withJSONHeader(write func(http.ResponseWriter)) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write(w)
	}
}

func legacyParticipants(w http.ResponseWriter, epoch uint64, records []ParticipantRecord) {
	writeJSON(w, http.StatusOK, participantsDocument{SchemaVersion: SchemaVersion, EpochIndex: epoch, Participants: records})
}

// Test flow:
//  1. Build records with every field non-zero and check the encoded form leaves no zero leaf.
//  2. For 0, 1 and 300 records, serve them through the single-encode path and through `writeParticipants` on real servers.
//  3. Assert the status, the headers and the body bytes are identical.
func TestWriteParticipantsMatchesSingleEncode(t *testing.T) {
	var probe any
	encoded, err := json.Marshal(fullRecords(1)[0])
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := json.Unmarshal(encoded, &probe); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	assertEveryLeafNonZero(t, "record", probe)

	for _, count := range []int{0, 1, 300} {
		records := fullRecords(count)
		if count == 0 {
			records = []ParticipantRecord{}
		}
		want := fetch(t, withJSONHeader(func(w http.ResponseWriter) { legacyParticipants(w, 41, records) }))
		got := fetch(t, withJSONHeader(func(w http.ResponseWriter) { writeParticipants(w, 41, records) }))

		if got.status != want.status {
			t.Errorf("%d records: status = %d, want %d", count, got.status, want.status)
		}
		if !reflect.DeepEqual(got.header, want.header) {
			t.Errorf("%d records: headers = %v, want %v", count, got.header, want.header)
		}
		if !bytes.Equal(got.body, want.body) {
			t.Errorf("%d records: body differs from the single-encode body (%d bytes, want %d)", count, len(got.body), len(want.body))
		}
	}
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and request the participants route.
//  2. Encode the same query with the single-encode path.
//  3. Assert the route's body equals that encoding byte for byte.
func TestParticipantsRouteBodyMatchesSingleEncode(t *testing.T) {
	book := twoEpochBook(t)

	got := serve(t, book, "/api/v1/epochs/current/participants")

	want := httptest.NewRecorder()
	legacyParticipants(want, testEpoch, book.Query(QueryFilter{EpochIndex: testEpoch}))
	if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), want.Body.Bytes()) {
		t.Errorf("route answered %d %q, want 200 %q", got.Code, got.Body.String(), want.Body.String())
	}
}

type maxWriteSink struct {
	header  http.Header
	largest int
}

func (s *maxWriteSink) Header() http.Header { return s.header }
func (s *maxWriteSink) WriteHeader(int)     {}
func (s *maxWriteSink) Write(payload []byte) (int, error) {
	s.largest = max(s.largest, len(payload))
	return len(payload), nil
}

func benchParticipantsWrite(b *testing.B, write func(http.ResponseWriter, uint64, []ParticipantRecord)) {
	records := fullRecords(2000)
	sink := &maxWriteSink{header: http.Header{}}
	b.ReportAllocs()
	for b.Loop() {
		write(sink, 41, records)
	}
	b.ReportMetric(float64(sink.largest), "largest-write-B")
}

// Test flow:
//  1. Encode 2000 full records with the single-encode path into a sink that discards the body.
//  2. Report allocated bytes per response and the largest single write, which is the whole body.
func BenchmarkParticipantsSingleEncode(b *testing.B) { benchParticipantsWrite(b, legacyParticipants) }

// Test flow:
//  1. Encode the same 2000 full records with `writeParticipants` into the same sink.
//  2. Report allocated bytes per response and the largest single write, which is one record.
func BenchmarkParticipantsStreamed(b *testing.B) { benchParticipantsWrite(b, writeParticipants) }
