package quant

import (
	"reflect"
	"testing"

	"quantlab/internal/domain"
)

func TestExtractCloses(t *testing.T) {
	in := []domain.Bar{
		{OpenTime: 1000, Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 100},
		{OpenTime: 2000, Open: 1.5, High: 3, Low: 1.4, Close: 2.7, Volume: 200},
		{OpenTime: 3000, Open: 2.7, High: 2.8, Low: 2.5, Close: 2.6, Volume: 150},
	}
	got := ExtractCloses(in)
	want := []float64{1.5, 2.7, 2.6}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExtractCloses = %v, want %v", got, want)
	}
}

func TestExtractCloses_Empty(t *testing.T) {
	got := ExtractCloses(nil)
	if len(got) != 0 {
		t.Errorf("ExtractCloses(nil) = %v, want []", got)
	}
}

func TestExtractTimestamps(t *testing.T) {
	in := []domain.Bar{
		{OpenTime: 1000},
		{OpenTime: 2000},
		{OpenTime: 3000},
	}
	got := ExtractTimestamps(in)
	want := []int64{1000, 2000, 3000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExtractTimestamps = %v, want %v", got, want)
	}
}
