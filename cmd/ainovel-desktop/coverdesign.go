package main

import "strings"

type coverDesignDefinition struct {
	Direction   string
	Instruction string
}

var coverPlatformDefinitions = map[string]coverDesignDefinition{
	"tomato": {
		Direction: "Fanqie-style Chinese mass-market mobile web-fiction cover, vibrant saturated complementary colors, " +
			"high contrast, an expressive character or relationship pair occupying 60 to 72 percent of the frame, " +
			"clear well-lit face, one instantly readable dramatic hook, glossy commercial digital illustration, " +
			"strong silhouette and clean hierarchy that remains legible as a small app thumbnail.",
		Instruction: "番茄小说：人物占画面 60%-72%，面部清楚，高饱和高对比，冲突和情绪一眼可读；标题区保留有颜色、有光线的低细节氛围，禁止整块黑色空白。",
	},
	"qidian": {
		Direction: "Qidian-style premium Chinese web-fiction cover, polished refined illustration, detailed cinematic composition, " +
			"mature color palette, balanced character and environment, layered depth and epic atmosphere.",
		Instruction: "起点：细腻写实插画，人物与场景均衡，层次丰富、色彩沉稳，强调成熟的电影感。",
	},
	"jinjiang": {
		Direction: "Jinjiang-style elegant romantic web-fiction cover, dreamy ethereal aesthetic, soft pastel colors, " +
			"delicate character beauty, clean centered composition, subtle petals, silk or bokeh accents.",
		Instruction: "晋江：柔和粉紫、浅蓝或暖白色调，人物精致、构图干净，使用克制的花瓣、丝绸或光斑装饰。",
	},
	"zhihu": {
		Direction: "Zhihu Yanyan-style minimalist literary cover, restrained cool palette, symbolic scene or object, " +
			"generous but textured negative space, subtle moody atmosphere and independent film poster quality.",
		Instruction: "知乎盐言：极简冷调，以场景、物件或抽象意象承载主题，留白要有细微纹理和氛围，不做人物素材堆叠。",
	},
	"qimao": {
		Direction: "Qimao-style high-impact web-fiction cover, extremely vivid dramatic colors, ornate costume or equipment, " +
			"spectacular but controlled energy effects and attention-grabbing poster composition.",
		Instruction: "七猫：高饱和强冲击，服饰装备华丽，允许火焰、雷电或灵力特效，但主体层级必须清楚。",
	},
	"ciweimao": {
		Direction: "Ciweimao-style Japanese light-novel cover, vibrant anime illustration, crisp line art, expressive character design, " +
			"bright colors and playful graphic accents.",
		Instruction: "刺猬猫：日系轻小说插画，线稿清晰、色彩明亮、角色表情鲜明，可使用少量活泼图形装饰。",
	},
}

var coverGenreDefinitions = map[string]coverDesignDefinition{
	"xianxia": {
		Direction: "Xianxia Chinese fantasy: flowing period robes, expressive martial posture, a distinctive sword or spiritual artifact, " +
			"layered immortal mountains and architecture, deep blue, jade, white and gold palette, directional divine light and restrained spiritual particles.",
		Instruction: "玄幻仙侠：明确服饰、姿态和法器，前景人物、中景建筑、远景仙山分层，以青蓝、玉色、白和金色构成主色。",
	},
	"urban": {
		Direction: "Contemporary urban fiction: recognizable modern clothing and occupation-specific props, confident readable expression, " +
			"city skyline, office, campus or neon street context, deep blue and grey with one warm gold or orange accent.",
		Instruction: "都市：人物职业、服装和道具必须具体，背景给出可识别的城市空间，深蓝灰为主并用一处暖色点题。",
	},
	"ancient_romance": {
		Direction: "Ancient Chinese romance and palace drama: historically grounded ornate costume, elegant restrained pose, " +
			"palace courtyard, red walls, screens or lanterns, rich cinnabar red, antique gold and ink black, warm directional lantern light.",
		Instruction: "古言宫斗：华服和时代身份准确，以宫殿、红墙、屏风或灯笼建立环境，正红、古金和墨黑形成华贵层次。",
	},
	"modern_romance": {
		Direction: "Modern romance: two clearly differentiated protagonists in an emotionally legible interaction, fashionable contemporary styling, " +
			"warm intimate setting, blush pink, warm white and pale gold palette, soft backlight without excessive bokeh.",
		Instruction: "现言甜宠：双人关系和情绪动作优先，现代穿搭清楚，以粉、暖白和浅金营造亲密感，避免廉价光斑堆满画面。",
	},
	"suspense": {
		Direction: "Mystery thriller: one concrete clue or threatening anomaly, tense character posture or silhouette, layered urban or enclosed setting, " +
			"charcoal and deep teal balanced by a visible red, amber or cold-white focal light, illustrated noir atmosphere without crushing blacks.",
		Instruction: "悬疑推理：必须出现一个具体线索或异常点，黑灰和深青只能做基底，必须用红、琥珀或冷白主光拉开人物与背景，禁止死黑。",
	},
	"scifi": {
		Direction: "Science fiction or apocalypse: specific technology, tactical gear or survival prop, ruined city, laboratory, station or spacecraft in layers, " +
			"deep blue and graphite with electric cyan, violet or energy-green accents, crisp holographic rim light.",
		Instruction: "科幻末世：科技装置、战术装备或生存道具要具体，环境分层，深蓝石墨配电光青、紫或能量绿。",
	},
	"western_fantasy": {
		Direction: "Western high fantasy: distinctive armor, robe or ranger gear, dynamic weapon or magic gesture, castle, dragon lair or wild landscape, " +
			"stormy blue, antique gold and silver with controlled fire-red or magic-violet accents.",
		Instruction: "西幻：人物职业装备和动作明确，城堡、龙巢或原野建立世界，暗金银蓝为主并用火红或魔法紫点亮。",
	},
	"historical": {
		Direction: "Historical Chinese epic: period-accurate general, strategist or civilian figure, decisive gesture, layered city wall, battlefield or court setting, " +
			"iron grey, earth ochre and dark red with smoky sunset or firelight.",
		Instruction: "历史军事：人物身份、服饰和器物符合时代，以城墙、战场或朝堂建立层次，铁灰土黄配暗红和烽火光。",
	},
	"supernatural": {
		Direction: "Chinese supernatural horror: a specific ritual object or uncanny figure, restrained fear response, old temple, alley, graveyard or folk interior, " +
			"ink black, desaturated green and paper white with one candle-yellow or dark-red accent, eerie illustrated lighting without an unreadable black frame.",
		Instruction: "灵异恐怖：用具体仪式物件或异常形象制造不安，墨黑幽绿配纸白、烛黄或暗红，恐怖不等于整张压黑。",
	},
	"light_novel": {
		Direction: "Anime light novel: highly expressive character design, crisp colorful line art, lively pose, fantasy, campus or otherworld setting, " +
			"bright multi-color palette with controlled stars, petals or magical particles.",
		Instruction: "轻小说：角色设计和表情优先，线稿清晰、姿态活泼、色彩明亮，星光花瓣等装饰只做点缀。",
	},
}

var coverCompositionDefinitions = map[string]coverDesignDefinition{
	"portrait": {
		Direction:   "Close or medium character portrait with the face and signature prop immediately readable.",
		Instruction: "人物特写：脸、表情和标志性道具必须在缩略图中清楚。",
	},
	"dynamic": {
		Direction:   "Full or three-quarter body character in a dynamic story-specific action, with clear silhouette and directional movement.",
		Instruction: "全身动态：动作必须与故事身份相关，轮廓清楚，并形成明确运动方向。",
	},
	"scene": {
		Direction:   "Atmospheric environment or symbolic-object composition, with one visual clue and no generic character collage.",
		Instruction: "氛围场景：用环境或象征物承载故事，保留一个清楚的视觉线索，不做人像拼贴。",
	},
	"duo": {
		Direction:   "Two-character relationship composition with distinct silhouettes, readable eye line or physical tension, and one shared story context.",
		Instruction: "双人关系：两人轮廓和身份可区分，通过视线、距离或动作直接表达关系张力。",
	},
}

func normalizeCoverPlatform(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if _, ok := coverPlatformDefinitions[platform]; ok {
		return platform
	}
	// 兼容上一版把视觉风格写进 preset 的元数据。
	switch platform {
	case "cinematic", "guofeng":
		return "qidian"
	case "literary":
		return "zhihu"
	default:
		return defaultCoverStylePreset
	}
}

func normalizeCoverGenre(genre string) string {
	genre = strings.ToLower(strings.TrimSpace(genre))
	if genre == "" || genre == "auto" {
		return defaultCoverGenre
	}
	if _, ok := coverGenreDefinitions[genre]; ok {
		return genre
	}
	return defaultCoverGenre
}

func normalizeCoverComposition(composition string) string {
	composition = strings.ToLower(strings.TrimSpace(composition))
	if composition == "" || composition == "auto" {
		return defaultCoverComposition
	}
	if _, ok := coverCompositionDefinitions[composition]; ok {
		return composition
	}
	return defaultCoverComposition
}

func resolveCoverComposition(composition, genre string) string {
	composition = normalizeCoverComposition(composition)
	if composition != "auto" {
		return composition
	}
	switch genre {
	case "modern_romance", "ancient_romance":
		return "duo"
	case "xianxia", "western_fantasy", "historical", "light_novel":
		return "dynamic"
	case "scifi":
		return "scene"
	default:
		return "portrait"
	}
}

type coverGenreRule struct {
	Genre    string
	Keywords []string
}

var coverGenreRules = []coverGenreRule{
	{Genre: "xianxia", Keywords: []string{"仙", "修真", "修仙", "剑道", "灵根", "宗门", "天帝", "神尊", "xianxia", "cultivation"}},
	{Genre: "western_fantasy", Keywords: []string{"魔法", "异世界", "精灵", "骑士", "领主", "龙族", "wizard", "dragon", "fantasy"}},
	{Genre: "ancient_romance", Keywords: []string{"王妃", "皇后", "嫡女", "庶女", "宫斗", "侯府", "朝堂", "凤冠", "古言"}},
	{Genre: "modern_romance", Keywords: []string{"总裁", "契约", "替嫁", "甜宠", "娇妻", "萌宝", "闪婚", "白月光", "现言", "romance"}},
	{Genre: "urban", Keywords: []string{"都市", "校园", "学霸", "医生", "兵王", "职场", "娱乐圈", "直播", "外卖", "urban"}},
	{Genre: "suspense", Keywords: []string{"悬疑", "推理", "侦探", "刑侦", "密室", "连环", "失踪", "尸体", "凶手", "调查", "谜", "异常", "地铁", "隧道", "倒计时", "thriller", "mystery"}},
	{Genre: "scifi", Keywords: []string{"星际", "末世", "机甲", "赛博", "废土", "进化", "空间站", "人工智能", "sci-fi", "cyberpunk"}},
	{Genre: "historical", Keywords: []string{"三国", "大明", "大唐", "战场", "将军", "谋士", "军营", "历史", "historical"}},
	{Genre: "supernatural", Keywords: []string{"灵异", "鬼", "僵尸", "阴阳", "风水", "盗墓", "诅咒", "纸人", "古庙", "supernatural", "horror"}},
	{Genre: "light_novel", Keywords: []string{"萌系", "喵", "团宠", "转生", "二次元", "轻小说", "猫耳", "anime"}},
}

func inferCoverGenre(text string) string {
	text = strings.ToLower(text)
	bestGenre, bestScore := "urban", 0
	for _, rule := range coverGenreRules {
		score := 0
		for _, keyword := range rule.Keywords {
			if strings.Contains(text, strings.ToLower(keyword)) {
				score++
			}
		}
		if score > bestScore {
			bestGenre, bestScore = rule.Genre, score
		}
	}
	return bestGenre
}

func coverPromptHardRulesFor(platform string) string {
	ratio := "2:3"
	if normalizeCoverPlatform(platform) == "tomato" {
		ratio = "3:4"
	}
	return "Portrait " + ratio + " book-cover composition designed to remain clear at 180-pixel thumbnail width. " +
		"Keep one dominant focal point and reserve the upper 26 to 30 percent as a title-safe, low-detail area that still contains atmospheric color, light and texture; never create a flat solid-color or near-black empty block. " +
		"Digital painting or polished commercial illustration, not a photograph and not a film still. " +
		"No text, no letters, no words, no watermark, no signature. No book mockup, border, frame, collage or tiny decorative clutter."
}

func buildCoverGenerationPrompt(content, platform, genre, composition string) string {
	platform = normalizeCoverPlatform(platform)
	genre = normalizeCoverGenre(genre)
	if genre == "auto" {
		genre = inferCoverGenre(content)
	}
	composition = resolveCoverComposition(composition, genre)
	parts := []string{
		strings.TrimSpace(content),
		coverPlatformDefinitions[platform].Direction,
		coverGenreDefinitions[genre].Direction,
		coverCompositionDefinitions[composition].Direction,
		coverPromptHardRulesFor(platform),
	}
	return strings.Join(parts, " ")
}

func coverPromptSystemFor(platform, genre, composition string) string {
	platform = normalizeCoverPlatform(platform)
	genre = normalizeCoverGenre(genre)
	if genre == "auto" {
		genre = "urban"
	}
	composition = resolveCoverComposition(composition, genre)
	return coverPromptSystem +
		"\n7. 平台视觉方向：" + coverPlatformDefinitions[platform].Instruction +
		"\n8. 题材视觉方向：" + coverGenreDefinitions[genre].Instruction +
		"\n9. 构图方向：" + coverCompositionDefinitions[composition].Instruction +
		"\n10. 即使用户旧提示词要求纯黑、摄影或大面积空白，也必须按以上规则改写，不得保留冲突要求。"
}

func imageGenSizeForPlatform(configured, platform string) string {
	// 平台上传尺寸由本地交付层负责。这里保留用户配置的接口尺寸，避免把
	// 768x1024 等兼容网关尺寸强行发给只接受固定尺寸的官方模型。
	_ = platform
	return configured
}

func titleStyleForCoverGenre(genre string) string {
	switch normalizeCoverGenre(genre) {
	case "xianxia", "western_fantasy", "ancient_romance", "historical":
		return "gold"
	case "modern_romance", "light_novel":
		return "romance"
	case "suspense", "supernatural":
		return "thriller"
	case "scifi":
		return "scifi"
	case "urban":
		return "modern"
	default:
		return "modern"
	}
}
