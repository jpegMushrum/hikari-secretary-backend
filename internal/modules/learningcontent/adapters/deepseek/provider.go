package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	content "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent/domain"
)

var errIncompleteTopic = errors.New("DeepSeek вернул пустой или некорректный JSON материала")

type Provider struct {
	apiKey, model, baseURL                       string
	questionCount, vocabularyCount, grammarCount int
	client                                       *http.Client
}

func NewProvider(apiKey, model, baseURL string, timeout time.Duration, questions, vocabulary, grammar int) *Provider {
	return &Provider{strings.TrimSpace(apiKey), model, strings.TrimRight(baseURL, "/"), questions, vocabulary, grammar, &http.Client{Timeout: timeout}}
}

func (p *Provider) Generate(ctx context.Context, history []content.Topic, options content.Options) (content.Topic, error) {
	used := make([]map[string]string, 0, len(history))
	grammar := map[string]int{}
	vocabulary := map[string]int{}
	for _, item := range history {
		used = append(used, map[string]string{"conversation_topic": item.Title, "vocabulary_theme": item.VocabularyTheme})
		for _, value := range item.Grammar {
			if pattern := strings.TrimSpace(value.Pattern); pattern != "" {
				grammar[pattern]++
			}
		}
		for _, value := range item.Vocabulary {
			if word := strings.TrimSpace(value.Japanese); word != "" {
				vocabulary[word]++
			}
		}
	}
	usedJSON, _ := json.Marshal(used)
	languageJSON, _ := json.Marshal(map[string]map[string]int{"grammar_patterns": grammar, "vocabulary_words": vocabulary})
	comment := ""
	if options.AdminComment != nil && strings.TrimSpace(*options.AdminComment) != "" {
		comment = " Пожелание администратора: " + strings.TrimSpace(*options.AdminComment)
	}
	guidance := map[string]string{"N5": "базовые короткие фразы", "N4": "элементарные связные фразы", "N3": "средний уровень: причины, сравнения и выражение мнения; конструкции N3, а не только N5-N4", "N2": "сложные связные высказывания", "N1": "продвинутая абстрактная лексика"}[options.JLPTLevel]
	system := fmt.Sprintf("Ты составляешь ежедневный материал для разговорного клуба японского языка. Верни только JSON без Markdown. Материал должен точно соответствовать JLPT %s (%s), не упрощай его до более низкого уровня. Название темы (title) пиши по-японски. Объяснения и переводы пиши по-русски, примеры — по-японски. Не повторяй темы из истории.%s", options.JLPTLevel, guidance, comment)
	user := fmt.Sprintf("История тем: %s\nСлова и грамматические конструкции из всех сохранённых версий с числом употреблений: %s. Для разнообразия предпочитай новые и редко использованные элементы подходящего уровня.\nВерни JSON с полями jlpt_level, title, vocabulary_theme, introduction, questions, vocabulary[{japanese,reading,translation}], grammar[{pattern,explanation,example,translation}]. Уровень jlpt_level строго %s. Дай ровно %d вопросов, %d слов и %d конструкций. Название до 40 символов, введение до 100, каждый вопрос до 80. Не добавляй текст вне JSON.", usedJSON, languageJSON, options.JLPTLevel, p.questionCount, p.vocabularyCount, p.grammarCount)
	user += ` Пример формата (число элементов и уровень возьми из задания): {"jlpt_level":"N3","title":"旅の思い出","vocabulary_theme":"Путешествия","introduction":"Обсудим поездки.","questions":["どこに行きましたか。"],"vocabulary":[{"japanese":"旅","reading":"たび","translation":"путешествие"}],"grammar":[{"pattern":"〜たことがある","explanation":"Опыт в прошлом","example":"京都に行ったことがあります。","translation":"Я бывал в Киото."}]}`
	for attempt := 0; attempt < 2; attempt++ {
		if attempt == 1 {
			user += "\nПредыдущий ответ оборвался. Пересоздай полный JSON короче, сохрани число элементов и уровень."
		}
		topic, finishReason, err := p.request(ctx, system, user)
		if err == nil {
			if topic.JLPTLevel != options.JLPTLevel {
				return content.Topic{}, fmt.Errorf("DeepSeek returned %s, expected %s", topic.JLPTLevel, options.JLPTLevel)
			}
			if err = topic.Validate(p.questionCount, p.vocabularyCount, p.grammarCount); err != nil {
				return content.Topic{}, err
			}
			return topic, nil
		}
		if finishReason != "length" && !errors.Is(err, errIncompleteTopic) {
			return content.Topic{}, err
		}
	}
	return content.Topic{}, fmt.Errorf("DeepSeek дважды не вернул полный JSON материала")
}

func (p *Provider) request(ctx context.Context, system, user string) (content.Topic, string, error) {
	payload := map[string]any{"model": p.model, "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}, "response_format": map[string]string{"type": "json_object"}, "max_tokens": 4000, "stream": false}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return content.Topic{}, "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return content.Topic{}, "", fmt.Errorf("DeepSeek request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return content.Topic{}, "", err
	}
	if resp.StatusCode >= 400 {
		return content.Topic{}, "", fmt.Errorf("DeepSeek HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var completion struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err = json.Unmarshal(raw, &completion); err != nil || len(completion.Choices) == 0 {
		return content.Topic{}, "", fmt.Errorf("invalid DeepSeek completion")
	}
	finishReason := completion.Choices[0].FinishReason
	if finishReason != "stop" {
		return content.Topic{}, finishReason, fmt.Errorf("DeepSeek did not finish: %s", finishReason)
	}
	var topic content.Topic
	if err = json.Unmarshal([]byte(completion.Choices[0].Message.Content), &topic); err != nil {
		return content.Topic{}, finishReason, errIncompleteTopic
	}
	return topic, finishReason, nil
}
