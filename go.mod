module loger-go

go 1.14

require (
	github.com/go-sql-driver/mysql v1.6.0
	github.com/jinzhu/gorm v1.9.16
	github.com/mediocregopher/radix.v2 v0.0.0-20181115013041-b67df6e626f9
	github.com/mgutz/str v1.2.0
	github.com/robfig/cron/v3 v3.0.1
	github.com/sirupsen/logrus v1.8.1
)

replace gotest => ../gotest
