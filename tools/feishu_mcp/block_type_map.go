package feishu_mcp

// Block type constants matching Feishu Docx API.
const (
	BlockTypePage            = 1
	BlockTypeText            = 2
	BlockTypeHeading1        = 3
	BlockTypeHeading2        = 4
	BlockTypeHeading3        = 5
	BlockTypeHeading4        = 6
	BlockTypeHeading5        = 7
	BlockTypeHeading6        = 8
	BlockTypeHeading7        = 9
	BlockTypeHeading8        = 10
	BlockTypeHeading9        = 11
	BlockTypeBullet          = 12
	BlockTypeOrdered         = 13
	BlockTypeCode            = 14
	BlockTypeQuote           = 15
	BlockTypeTodo            = 17
	BlockTypeBitable         = 18
	BlockTypeCallout         = 19
	BlockTypeChatCard        = 20
	BlockTypeDiagram         = 21
	BlockTypeDivider         = 22
	BlockTypeFile            = 23
	BlockTypeGrid            = 24
	BlockTypeGridColumn      = 25
	BlockTypeIframe          = 26
	BlockTypeImage           = 27
	BlockTypeISV             = 28
	BlockTypeMindnote        = 29
	BlockTypeSheet           = 30
	BlockTypeTable           = 31
	BlockTypeTableCell       = 32
	BlockTypeView            = 33
	BlockTypeQuoteContainer  = 34
	BlockTypeTask            = 35
	BlockTypeOKR             = 36
	BlockTypeOKRObjective    = 37
	BlockTypeOKRKeyResult    = 38
	BlockTypeOKRProgress     = 39
	BlockTypeAddOns          = 40
	BlockTypeJiraIssue       = 41
	BlockTypeWikiCatalog     = 42
	BlockTypeBoard           = 43
	BlockTypeAgenda          = 44
	BlockTypeAgendaItem      = 45
	BlockTypeAgendaItemTitle = 46
	BlockTypeAgendaContent   = 47
	BlockTypeLinkPreview     = 48
	BlockTypeSourceSynced    = 49
	BlockTypeReferenceSynced = 50
	BlockTypeSubPageList     = 51
	BlockTypeAITemplate      = 52
	BlockTypeUndefined       = 999
)

// BlockTypeName maps block type ID to its JSON keyword.
var BlockTypeName = map[int]string{
	BlockTypePage:            "page",
	BlockTypeText:            "text",
	BlockTypeHeading1:        "heading1",
	BlockTypeHeading2:        "heading2",
	BlockTypeHeading3:        "heading3",
	BlockTypeHeading4:        "heading4",
	BlockTypeHeading5:        "heading5",
	BlockTypeHeading6:        "heading6",
	BlockTypeHeading7:        "heading7",
	BlockTypeHeading8:        "heading8",
	BlockTypeHeading9:        "heading9",
	BlockTypeBullet:          "bullet",
	BlockTypeOrdered:         "ordered",
	BlockTypeCode:            "code",
	BlockTypeQuote:           "quote",
	BlockTypeTodo:            "todo",
	BlockTypeBitable:         "bitable",
	BlockTypeCallout:         "callout",
	BlockTypeChatCard:        "chat_card",
	BlockTypeDiagram:         "diagram",
	BlockTypeDivider:         "divider",
	BlockTypeFile:            "file",
	BlockTypeGrid:            "grid",
	BlockTypeGridColumn:      "grid_column",
	BlockTypeIframe:          "iframe",
	BlockTypeImage:           "image",
	BlockTypeISV:             "isv",
	BlockTypeMindnote:        "mindnote",
	BlockTypeSheet:           "sheet",
	BlockTypeTable:           "table",
	BlockTypeTableCell:       "table_cell",
	BlockTypeView:            "view",
	BlockTypeQuoteContainer:  "quote_container",
	BlockTypeTask:            "task",
	BlockTypeOKR:             "okr",
	BlockTypeOKRObjective:    "okr_objective",
	BlockTypeOKRKeyResult:    "okr_key_result",
	BlockTypeOKRProgress:     "okr_progress",
	BlockTypeAddOns:          "add_ons",
	BlockTypeJiraIssue:       "jira_issue",
	BlockTypeWikiCatalog:     "wiki_catalog",
	BlockTypeBoard:           "board",
	BlockTypeAgenda:          "agenda",
	BlockTypeAgendaItem:      "agenda_item",
	BlockTypeAgendaItemTitle: "agenda_item_title",
	BlockTypeAgendaContent:   "agenda_item_content",
	BlockTypeLinkPreview:     "link_preview",
	BlockTypeSourceSynced:    "source_synced",
	BlockTypeReferenceSynced: "reference_synced",
	BlockTypeSubPageList:     "sub_page_list",
	BlockTypeAITemplate:      "ai_template",
	BlockTypeUndefined:       "undefined",
}

// BlockTypeDesc maps block type ID to its Chinese description.
var BlockTypeDesc = map[int]string{
	BlockTypePage:            "页面",
	BlockTypeText:            "文本",
	BlockTypeHeading1:        "标题 1",
	BlockTypeHeading2:        "标题 2",
	BlockTypeHeading3:        "标题 3",
	BlockTypeHeading4:        "标题 4",
	BlockTypeHeading5:        "标题 5",
	BlockTypeHeading6:        "标题 6",
	BlockTypeHeading7:        "标题 7",
	BlockTypeHeading8:        "标题 8",
	BlockTypeHeading9:        "标题 9",
	BlockTypeBullet:          "无序列表",
	BlockTypeOrdered:         "有序列表",
	BlockTypeCode:            "代码块",
	BlockTypeQuote:           "引用",
	BlockTypeTodo:            "待办事项",
	BlockTypeBitable:         "多维表格",
	BlockTypeCallout:         "高亮块",
	BlockTypeChatCard:        "会话卡片",
	BlockTypeDiagram:         "流程图 & UML",
	BlockTypeDivider:         "分割线",
	BlockTypeFile:            "文件",
	BlockTypeGrid:            "分栏",
	BlockTypeGridColumn:      "分栏列",
	BlockTypeIframe:          "内嵌 Block",
	BlockTypeImage:           "图片",
	BlockTypeISV:             "开放平台小组件",
	BlockTypeMindnote:        "思维笔记",
	BlockTypeSheet:           "电子表格",
	BlockTypeTable:           "表格",
	BlockTypeTableCell:       "表格单元格",
	BlockTypeView:            "视图",
	BlockTypeQuoteContainer:  "引用容器",
	BlockTypeTask:            "任务",
	BlockTypeOKR:             "OKR",
	BlockTypeOKRObjective:    "OKR Objective",
	BlockTypeOKRKeyResult:    "OKR Key Result",
	BlockTypeOKRProgress:     "OKR Progress",
	BlockTypeAddOns:          "新版文档小组件",
	BlockTypeJiraIssue:       "Jira 问题",
	BlockTypeWikiCatalog:     "Wiki 子页面列表（旧版）",
	BlockTypeBoard:           "画板",
	BlockTypeAgenda:          "议程",
	BlockTypeAgendaItem:      "议程项",
	BlockTypeAgendaItemTitle: "议程项标题",
	BlockTypeAgendaContent:   "议程项内容",
	BlockTypeLinkPreview:     "链接预览",
	BlockTypeSourceSynced:    "源同步块",
	BlockTypeReferenceSynced: "引用同步块",
	BlockTypeSubPageList:     "Wiki 子页面列表（新版）",
	BlockTypeAITemplate:      "AI 模板",
	BlockTypeUndefined:       "未支持",
}

// GetBlockTypeName returns the JSON keyword for a block type ID.
func GetBlockTypeName(blockType int) string {
	if name, ok := BlockTypeName[blockType]; ok {
		return name
	}
	return "undefined"
}

// GetBlockTypeDesc returns the Chinese description for a block type ID.
func GetBlockTypeDesc(blockType int) string {
	if desc, ok := BlockTypeDesc[blockType]; ok {
		return desc
	}
	return "未支持"
}
