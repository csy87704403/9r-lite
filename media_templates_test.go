package main

import (
	"strings"
	"testing"
)

func TestAgnesImageTemplatesIncludeImageEditingModes(t *testing.T) {
	p := ProviderConfig{ID: "custom3", Name: "Agnes", Type: "openai"}
	template := healthMediaTemplateForProvider(
		p,
		"image",
		"https://apihub.agnes-ai.com/v1/images/generations",
		"agnes-image-2.0-flash",
		false,
	)

	if len(template.ExtraTemplates) != 3 {
		t.Fatalf("extra template count = %d, want 3", len(template.ExtraTemplates))
	}
	wantNames := []string{
		"图生图 / 图片编辑：URL 输入，URL 输出",
		"图生图 / 图片编辑：Data URI Base64 输入，Base64 输出",
		"多图合成：多个 URL 输入，URL 输出",
	}
	for i, want := range wantNames {
		if template.ExtraTemplates[i].Name != want {
			t.Fatalf("template %d name = %q, want %q", i, template.ExtraTemplates[i].Name, want)
		}
		if !strings.Contains(template.ExtraTemplates[i].Curl, "extra_body") {
			t.Fatalf("template %d curl does not include extra_body", i)
		}
	}
	if !strings.Contains(template.ExtraTemplates[1].Curl, "data:image/png;base64,BASE64_HERE") {
		t.Fatal("base64 image edit template is missing Data URI input")
	}
}

func TestAgnesVideoTemplatesIncludeImageToVideoModes(t *testing.T) {
	p := ProviderConfig{ID: "custom3", Name: "Agnes", Type: "openai"}
	template := healthMediaTemplateForProvider(
		p,
		"video",
		"https://apihub.agnes-ai.com/v1/videos",
		"agnes-video-v2.0",
		false,
	)

	if len(template.ExtraTemplates) != 4 {
		t.Fatalf("extra template count = %d, want 4", len(template.ExtraTemplates))
	}
	wantNames := []string{"单图生视频", "多图视频生成", "关键帧动画", "查询视频结果"}
	for i, want := range wantNames {
		if template.ExtraTemplates[i].Name != want {
			t.Fatalf("template %d name = %q, want %q", i, template.ExtraTemplates[i].Name, want)
		}
	}
	if _, ok := template.ExtraTemplates[0].RequestBody["image"]; !ok {
		t.Fatal("single-image template must use top-level image")
	}
	extra, ok := template.ExtraTemplates[2].RequestBody["extra_body"].(map[string]any)
	if !ok || extra["mode"] != "keyframes" {
		t.Fatalf("keyframe template extra_body = %#v", extra)
	}
}

func TestExplicitModelKindOverridesNameDetection(t *testing.T) {
	p := ProviderConfig{
		ID:   "sensenova",
		Name: "SenseNova",
		ModelKinds: map[string]string{
			"sensenova-u1-fast": "image",
			"looks-like-image":  "text",
		},
	}
	imageModels := mediaModelsForProviderKind(p, []string{"sensenova-u1-fast", "looks-like-image"}, "image")
	if len(imageModels) != 1 || imageModels[0] != "sensenova-u1-fast" {
		t.Fatalf("image models = %#v", imageModels)
	}
	chatModels := chatModelIDs(p, []string{"sensenova-u1-fast", "looks-like-image"})
	if len(chatModels) != 1 || chatModels[0] != "looks-like-image" {
		t.Fatalf("chat models = %#v", chatModels)
	}
}

func TestExplicitImageModelAppearsInHealthTemplates(t *testing.T) {
	p := ProviderConfig{
		ID:            "custom-sensenova",
		Name:          "SenseNova",
		Type:          "openai",
		Enabled:       true,
		ImageEndpoint: "https://example.com/v1/images/generations",
		Models:        []string{"sensenova-u1-fast"},
		EnabledModels: []string{"sensenova-u1-fast"},
		ModelKinds:    map[string]string{"sensenova-u1-fast": "image"},
	}
	s := &Server{config: Config{Providers: []ProviderConfig{p}}}
	templates := s.healthMediaTemplates(false)
	if len(templates) != 1 || templates[0].UpstreamModel != "sensenova-u1-fast" || templates[0].Type != "image" {
		t.Fatalf("media templates = %#v", templates)
	}
}
