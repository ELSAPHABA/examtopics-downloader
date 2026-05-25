package fetch

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"examtopics-downloader/internal/constants"
	"examtopics-downloader/internal/models"
	"examtopics-downloader/internal/utils"

	"github.com/PuerkitoBio/goquery"
	"github.com/cheggaaa/pb/v3"
)

func getDataFromLink(link string) *models.QuestionData {
	doc, err := ParseHTML(link, *client)
	if err != nil {
		log.Printf("Failed parsing HTML data from link: %v", err)
		return nil
	}

	var allQuestions []string
	doc.Find("li.multi-choice-item").Each(func(i int, s *goquery.Selection) {
		allQuestions = append(allQuestions, utils.CleanText(s.Text()))
	})

	answer := extractAnswer(doc.Selection, link)

	return &models.QuestionData{
		Title:        utils.CleanText(doc.Find("h1").Text()),
		Header:       strings.ReplaceAll(strings.TrimSpace(doc.Find(".question-discussion-header").Text()), "\t", ""),
		Content:      utils.CleanText(doc.Find(".card-text").Text()),
		Questions:    allQuestions,
		Answer:       answer,
		AnswerImages: answerImagesFromAnswer(answer),
		Timestamp:    utils.CleanText(doc.Find(".discussion-meta-data > i").Text()),
		QuestionLink: link,
		Comments:     utils.CleanText(doc.Find(".discussion-container").Text()),
	}
}

func extractAnswer(scope *goquery.Selection, baseURL string) string {
	if answerImage := extractAnswerImageURL(scope, baseURL); answerImage != "" {
		return answerImage
	}

	answerText := strings.TrimSpace(scope.Find(".question-answer .correct-answer").First().Text())
	if answerText == "" {
		answerText = strings.TrimSpace(scope.Find(".correct-answer").First().Text())
	}
	if answerText == "" {
		answerText = extractHiddenChoiceAnswer(scope)
	}
	return normalizeAnswer(answerText)
}

func extractAnswerImageURL(scope *goquery.Selection, baseURL string) string {
	src := strings.TrimSpace(scope.Find(".question-answer .correct-answer img").First().AttrOr("src", ""))
	if src == "" {
		src = strings.TrimSpace(scope.Find(".correct-answer img").First().AttrOr("src", ""))
	}
	if src == "" {
		return ""
	}

	imageURL := resolveURL(baseURL, src)
	if !isImageURL(imageURL) {
		return ""
	}

	return imageURL
}

func isImageURL(rawURL string) bool {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	path := strings.ToLower(parsedURL.Path)
	return strings.HasSuffix(path, ".png") ||
		strings.HasSuffix(path, ".jpg") ||
		strings.HasSuffix(path, ".jpeg") ||
		strings.HasSuffix(path, ".gif") ||
		strings.HasSuffix(path, ".webp")
}

func answerImagesFromAnswer(answer string) []string {
	if isImageURL(answer) {
		return []string{answer}
	}
	return nil
}

func hasUsableAnswers(questions []models.QuestionData) bool {
	for _, question := range questions {
		if question.Answer != "" || len(question.AnswerImages) > 0 {
			return true
		}
	}
	return false
}

func mergeAnswersFromExamView(questions []models.QuestionData, examQuestions []models.QuestionData) int {
	answersByKey := make(map[string]models.QuestionData)
	for _, examQuestion := range examQuestions {
		key := questionKey(examQuestion)
		if key == "" || (examQuestion.Answer == "" && len(examQuestion.AnswerImages) == 0) {
			continue
		}
		answersByKey[key] = examQuestion
	}

	merged := 0
	for i := range questions {
		if questions[i].Answer != "" || len(questions[i].AnswerImages) > 0 {
			continue
		}

		examQuestion, exists := answersByKey[questionKey(questions[i])]
		if !exists {
			continue
		}

		questions[i].Answer = examQuestion.Answer
		questions[i].AnswerImages = examQuestion.AnswerImages
		merged++
	}

	return merged
}

func questionKey(question models.QuestionData) string {
	candidates := []string{question.Header, question.Title}
	topic := ""
	number := ""

	for _, candidate := range candidates {
		if topic == "" {
			if matches := regexp.MustCompile(`(?i)topic\s*#?\s*:?\s*(\d+)`).FindStringSubmatch(candidate); len(matches) == 2 {
				topic = matches[1]
			}
		}
		if number == "" {
			if matches := regexp.MustCompile(`(?i)question\s*#?\s*:?\s*(\d+)`).FindStringSubmatch(candidate); len(matches) == 2 {
				number = matches[1]
			}
		}
	}

	if number == "" {
		return ""
	}
	if topic == "" {
		topic = "0"
	}
	return topic + ":" + number
}

func extractHiddenChoiceAnswer(scope *goquery.Selection) string {
	var letters []string
	scope.Find("li.multi-choice-item.correct-hidden").Each(func(i int, choice *goquery.Selection) {
		letter := strings.TrimSpace(choice.Find(".multi-choice-letter").First().AttrOr("data-choice-letter", ""))
		if letter == "" {
			letter = strings.TrimSuffix(strings.TrimSpace(choice.Find(".multi-choice-letter").First().Text()), ".")
		}
		if letter != "" {
			letters = append(letters, letter)
		}
	})
	return strings.Join(letters, "")
}

func normalizeAnswer(answerText string) string {
	answerText = strings.TrimSpace(answerText)
	answerText = strings.TrimPrefix(answerText, "Correct Answer:")
	answerText = strings.TrimPrefix(answerText, "Suggested Answer:")
	return strings.Join(strings.Fields(answerText), "")
}

func parseExamPage(doc *goquery.Document, pageURL string) []models.QuestionData {
	examTitle := getExamPageTitle(doc)
	var questions []models.QuestionData

	doc.Find(".exam-question-card").Each(func(i int, card *goquery.Selection) {
		header := utils.CleanText(card.Find(".card-header").First().Text())
		content := utils.CleanText(card.Find(".question-body > p.card-text").Not(".question-answer").First().Text())
		answer := extractAnswer(card, pageURL)

		var choices []string
		card.Find("li.multi-choice-item").Each(func(i int, choice *goquery.Selection) {
			choices = append(choices, utils.CleanText(choice.Text()))
		})

		title := header
		if examTitle != "" {
			title = strings.TrimSpace(examTitle + " " + header)
		}

		questions = append(questions, models.QuestionData{
			Title:        title,
			Header:       header,
			Content:      content,
			Questions:    choices,
			Answer:       answer,
			AnswerImages: answerImagesFromAnswer(answer),
			QuestionLink: pageURL,
		})
	})

	return questions
}

func getExamPageTitle(doc *goquery.Document) string {
	title := utils.CleanText(doc.Find("h1").First().Text())
	if title == "" {
		title = utils.CleanText(doc.Find("title").First().Text())
	}
	if before, _, found := strings.Cut(title, "|"); found {
		title = strings.TrimSpace(before)
	}
	title = strings.TrimSuffix(title, " - ExamTopics")
	return title
}

func getMatchingExamViewLinks(providerName, grepStr string) []string {
	baseURL := fmt.Sprintf("https://www.examtopics.com/exams/%s/", providerName)
	doc, err := ParseHTML(baseURL, *client)
	if err != nil {
		log.Printf("Failed to parse provider exam list: %v", err)
		return nil
	}

	var links []string
	doc.Find(".popular-exam-link").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists {
			return
		}
		if grepStr != "" && !utils.GrepString(href, grepStr) && !utils.GrepString(s.Text(), grepStr) {
			return
		}

		examURL := resolveURL(baseURL, href)
		links = append(links, strings.TrimRight(examURL, "/")+"/view/")
	})

	return utils.DeduplicateLinks(links)
}

func scrapeExamViewPages(providerName, grepStr string) []models.QuestionData {
	examLinks := getMatchingExamViewLinks(providerName, grepStr)
	var allQuestions []models.QuestionData

	for _, examLink := range examLinks {
		seenPages := make(map[string]struct{})
		pageURL := examLink

		for pageURL != "" {
			if _, exists := seenPages[pageURL]; exists {
				break
			}
			seenPages[pageURL] = struct{}{}

			doc, err := ParseHTML(pageURL, *client)
			if err != nil {
				log.Printf("Failed to parse exam view page %s: %v", pageURL, err)
				break
			}

			pageQuestions := parseExamPage(doc, pageURL)
			if len(pageQuestions) == 0 {
				break
			}
			allQuestions = append(allQuestions, pageQuestions...)

			pageURL = getNextExamPageURL(doc, pageURL)
		}
	}

	return allQuestions
}

func getNextExamPageURL(doc *goquery.Document, currentURL string) string {
	var nextURL string
	doc.Find("a").EachWithBreak(func(i int, s *goquery.Selection) bool {
		if !strings.Contains(utils.CleanText(s.Text()), "Next Questions") {
			return true
		}

		href, exists := s.Attr("href")
		if !exists {
			return true
		}
		nextURL = resolveURL(currentURL, href)
		return false
	})
	if nextURL != "" {
		return nextURL
	}

	return getSequentialExamPageURL(currentURL)
}

func getSequentialExamPageURL(currentURL string) string {
	parsedURL, err := url.Parse(currentURL)
	if err != nil {
		return ""
	}

	path := strings.TrimRight(parsedURL.Path, "/")
	pageMatcher := regexp.MustCompile(`/view/(\d+)$`)
	if matches := pageMatcher.FindStringSubmatch(path); len(matches) == 2 {
		pageNum, err := strconv.Atoi(matches[1])
		if err != nil {
			return ""
		}
		parsedURL.Path = pageMatcher.ReplaceAllString(path, fmt.Sprintf("/view/%d", pageNum+1)) + "/"
		return parsedURL.String()
	}

	if strings.HasSuffix(path, "/view") {
		parsedURL.Path = path + "/2/"
		return parsedURL.String()
	}

	return ""
}

func resolveURL(baseURL, href string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return href
	}

	ref, err := url.Parse(href)
	if err != nil {
		return href
	}

	return base.ResolveReference(ref).String()
}

var counter int = 0 //start counter at 1
func getJSONFromLink(link string) []*models.QuestionData {
	initialResp := FetchURL(link, *client)

	var githubResp map[string]any
	err := json.Unmarshal(initialResp, &githubResp)
	if err != nil {
		log.Printf("error unmarshalling GitHub API response: %v", err)
		return nil
	}

	downloadURL, ok := githubResp["download_url"].(string)
	if !ok {
		log.Printf("couldn't find download_url in GitHub API response")
		return nil
	}

	jsonResp := FetchURL(downloadURL, *client)

	var content models.JSONResponse
	err = json.Unmarshal(jsonResp, &content)
	if err != nil {
		log.Printf("error unmarshalling the questions data: %v", err)
		return nil
	}

	fmt.Println("Processing content from:", downloadURL)

	var questions []*models.QuestionData

	if content.PageProps.Questions == nil {
		log.Printf("no questions found in JSON content")
		return nil
	}

	for _, q := range content.PageProps.Questions {
		var comments string
		for _, discussion := range q.Discussion {
			comments += fmt.Sprintf("[%s] %s\n", discussion.Poster, discussion.Content)
		}

		var choicesHeader string
		var keys []string
		for key := range q.Choices {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			choicesHeader += fmt.Sprintf("**%s:** %s\n\n", key, q.Choices[key])
		}

		name := utils.GetNameFromLink(link)
		counter++
		answer := q.Answer
		if answer == "" {
			answer = q.AnswerET
		}

		questions = append(questions, &models.QuestionData{
			Title:        "Examtopics " + strings.ReplaceAll(name, ".json?ref=main", "") + " question #" + strconv.Itoa(counter),
			Header:       q.QuestionText,
			Content:      strings.Join(q.QuestionImages, "\n"),
			Questions:    []string{choicesHeader},
			Answer:       normalizeAnswer(answer),
			AnswerImages: q.AnswerImages,
			Timestamp:    q.Timestamp,
			QuestionLink: q.URL,
			Comments:     utils.CleanText(comments),
		})
	}

	return questions
}

func fetchAllPageLinksConcurrently(providerName, grepStr string, numPages, concurrency int) []string {
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	results := make(chan []string, numPages)
	bar := pb.StartNew(numPages)
	startTime := utils.StartTime()

	rateLimiter := utils.CreateRateLimiter(constants.RequestsPerSecond)
	defer rateLimiter.Stop()

	for i := 1; i <= numPages; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			<-rateLimiter.C

			url := fmt.Sprintf("https://www.examtopics.com/discussions/%s/%d", providerName, i)
			results <- getLinksFromPage(url, grepStr)
			bar.Increment()
		}(i)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// about 10 questions per examtopics page, we can preallocate
	all := make([]string, 0, numPages*10)
	for res := range results {
		all = append(all, res...)
	}

	bar.Finish()
	fmt.Printf("Scraping completed in %s.\n", utils.TimeSince(startTime))
	return all
}

// Main concurrent page scraping logic
func GetAllPages(providerName string, grepStr string) []models.QuestionData {
	var examQuestions []models.QuestionData
	if grepStr != "" {
		examQuestions = scrapeExamViewPages(providerName, grepStr)
		if len(examQuestions) > 10 {
			fmt.Printf("Found %d questions from exam view pages.\n", len(examQuestions))
			return examQuestions
		}
		if len(examQuestions) > 0 {
			fmt.Printf("Only found %d questions from exam view pages; falling back to discussion pages.\n", len(examQuestions))
		}
	}

	baseURL := fmt.Sprintf("https://www.examtopics.com/discussions/%s/", providerName)
	numPages := getMaxNumPages(baseURL)
	fmt.Printf("Fetching %d pages for provider '%s'\n", numPages, providerName)

	allLinks := fetchAllPageLinksConcurrently(providerName, grepStr, numPages, constants.MaxConcurrentRequests)

	unique := utils.DeduplicateLinks(allLinks)
	sortedLinks := utils.SortLinksByQuestionNumber(unique)

	fmt.Printf("Found %d unique matching links:\n", len(sortedLinks))

	var wg sync.WaitGroup
	sem := make(chan struct{}, constants.MaxConcurrentRequests)
	results := make([]*models.QuestionData, len(sortedLinks))
	startTime := utils.StartTime()
	bar := pb.StartNew(len(sortedLinks))

	rateLimiter := utils.CreateRateLimiter(constants.RequestsPerSecond)
	defer rateLimiter.Stop()

	for i, link := range sortedLinks {
		wg.Add(1)
		url := utils.AddToBaseUrl(link)

		go func(i int, url string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			<-rateLimiter.C

			data := getDataFromLink(url)
			if data != nil {
				results[i] = data
			}
			bar.Increment()
		}(i, url)
	}

	wg.Wait()
	bar.Finish()
	// Filter out nil entries
	var finalData []models.QuestionData
	for _, entry := range results {
		if entry != nil {
			finalData = append(finalData, *entry)
		}
	}

	if len(examQuestions) > 0 && hasUsableAnswers(examQuestions) {
		mergedAnswers := mergeAnswersFromExamView(finalData, examQuestions)
		if mergedAnswers > 0 {
			fmt.Printf("Merged %d answers from exam view pages.\n", mergedAnswers)
		}
		if mergedAnswers == 0 && len(finalData) == 0 {
			return examQuestions
		}
	}

	fmt.Printf("Scraping completed in %s.\n", utils.TimeSince(startTime))

	return finalData
}
