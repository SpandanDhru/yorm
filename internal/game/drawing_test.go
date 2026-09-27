package game

import (
	"reflect"
	"testing"
)

func TestUploadedURLs(t *testing.T) {
	s := NewState("ses_abc")
	for url, want := range map[string]bool{
		"/uploads/ses_abc/img_x.png":   true,
		"/uploads/map_x.png":           true, // from before uploads were per session
		"/uploads/ses_other/img_x.png": false,
		"/uploads/../x.png":            false,
		"/uploads/ses_abc/../x.png":    false,
		"/uploads/ses_abc/.hidden":     false,
		"/uploads/":                    false,
		"https://evil.example/x.png":   false,
		"/uploads/a\\b.png":            false,
	} {
		if got := s.uploaded(url); got != want {
			t.Errorf("uploaded(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestTokenImages(t *testing.T) {
	s := withCharacters() // tok_1 is Kai's plain token; tok_kai stands for his PC
	s.ID = "ses_1"
	img := `"/uploads/ses_1/img_a.png"`
	refuse(t, s, testEnv, ana, "set_token_image", `{"token":"tok_1","image":`+img+`}`, CodeForbidden)
	refuse(t, s, testEnv, kai, "set_token_image", `{"token":"tok_1","image":"https://evil.example/a.png"}`, CodeInvalidTarget)
	refuse(t, s, testEnv, kai, "set_token_image", `{"token":"tok_nope","image":`+img+`}`, CodeInvalidTarget)
	play(t, s, testEnv, kai, "set_token_image", `{"token":"tok_1","image":`+img+`}`)
	play(t, s, testEnv, kai, "set_token_image", `{"token":"tok_kai","image":`+img+`}`) // through his character
	if s.Tokens["tok_1"].Image != "/uploads/ses_1/img_a.png" || s.Tokens["tok_kai"].Image == "" {
		t.Fatalf("images = %q, %q", s.Tokens["tok_1"].Image, s.Tokens["tok_kai"].Image)
	}
	play(t, s, testEnv, dm, "set_token_image", `{"token":"tok_1","image":""}`)
	if s.Tokens["tok_1"].Image != "" {
		t.Fatal("clearing the image didn't")
	}
	evs := play(t, s, testEnv, dm, "place_token", `{"label":"Owl","at":{"x":1,"y":1},"image":`+img+`}`)
	if evs[0].(TokenPlaced).Token.Image == "" {
		t.Fatal("placed without its image")
	}
}

func TestDrawing(t *testing.T) {
	s := fixture() // 10x8 map
	refuse(t, s, testEnv, kai, "draw", `{"color":"#ff0000","width":0.1,"points":[0,0,1,1]}`, CodeForbidden)
	for _, args := range []string{
		`{"color":"red","width":0.1,"points":[0,0,1,1]}`,
		`{"color":"#ff0000","width":0,"points":[0,0,1,1]}`,
		`{"color":"#ff0000","width":5,"points":[0,0,1,1]}`,
		`{"color":"#ff0000","width":0.1,"points":[]}`,
		`{"color":"#ff0000","width":0.1,"points":[0,0,1]}`,
		`{"color":"#ff0000","width":0.1,"points":[0,0,10.5,1]}`,
		`{"color":"#ff0000","width":0.1,"points":[0,0,1,-1]}`,
	} {
		refuse(t, s, testEnv, dm, "draw", args, CodeInvalidTarget)
	}
	evs := play(t, s, testEnv, dm, "draw", `{"color":"#FF0000","width":0.15,"points":[0,0,2.5,3.25,10,8]}`)
	want := Drawing{ID: "drw_new", Color: "#ff0000", Width: 0.15, Points: []float64{0, 0, 2.5, 3.25, 10, 8}}
	if !reflect.DeepEqual(evs, []Payload{DrawingAdded{Drawing: want}}) || !reflect.DeepEqual(s.Map.Drawings, []Drawing{want}) {
		t.Fatalf("events %+v, drawings %+v", evs, s.Map.Drawings)
	}
	refuse(t, s, testEnv, dm, "erase_drawing", `{"id":"drw_nope"}`, CodeInvalidTarget)
	play(t, s, testEnv, dm, "erase_drawing", `{"id":"drw_new"}`)
	if len(s.Map.Drawings) != 0 {
		t.Fatal("not erased")
	}
	refuse(t, s, testEnv, dm, "clear_drawings", `{}`, CodeInvalidTarget)
	play(t, s, testEnv, dm, "draw", `{"color":"#00ff00","width":0.1,"points":[1,1,2,2]}`)

	// Changing the grid keeps drawings; a new map starts clean.
	play(t, s, testEnv, dm, "set_map", `{"image_url":"/uploads/map_1.png","cols":12,"rows":8,"keep_terrain":true}`)
	if len(s.Map.Drawings) != 1 {
		t.Fatal("regrid lost the drawings")
	}
	play(t, s, testEnv, dm, "set_map", `{"cols":12,"rows":8}`)
	if len(s.Map.Drawings) != 0 {
		t.Fatal("a new map kept the old drawings")
	}
	// Players see drawings, and a stroke is the same event for them.
	play(t, s, testEnv, dm, "draw", `{"color":"#00ff00","width":0.1,"points":[1,1,2,2]}`)
	if len(s.View(kaiView).Map.Drawings) != 1 {
		t.Fatal("Kai doesn't see the drawing")
	}
	play(t, s, testEnv, dm, "clear_drawings", `{}`)
	if s.Map.Drawings != nil {
		t.Fatal("not cleared")
	}
}
