package main

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jinzhu/gorm"
	"github.com/mediocregopher/radix.v2/pool"
	"github.com/mgutz/str"
	"github.com/sirupsen/logrus"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const HANDLE_DIG 	= " /nginx_access.log?"
const HANDLE_MOVIE 	= "/movie/"
const HANDLE_LIST 	= "/list/"
const HANDLE_HTML 	= ".html"

//存放用户CMD输入的结构体
type cmdParams struct {
	logFilePath string
	routineNum	int
}

//日志格式的结构体
type urlData struct {
	data	logData  //数据内容,上报数据格式
	uid     string
	unode	urlNode
}

//按照上报格式定义的日志内容结构体
type logData struct {
	time	string
	url		string
	refer	string
	ua 		string
}

type urlNode struct {
	unType	string	//区分页面
	unRid	int		//资源ID
	unUrl	string	//当前页面url
	unTime	string	//访问时间
}

//日志存储
type storageBlock struct {
	counterType		string	//区分是PV还是UV统计
	storageModel	string	//存储格式
	unode			urlNode //存储内容
}
//数据库实例
var DBInstance *gorm.DB

type Logger struct {
	//gorm.Model
	Type  string
	Value int
}


//初始化日志打印
var log = logrus.New()

func init()  {
	log.Out = os.Stdout
	log.SetLevel(logrus.DebugLevel) //日志输出的级别
	log.Println("Init Databases.....")
	//修改默认的表明规则
	gorm.DefaultTableNameHandler = func (db *gorm.DB, defaultTableName string) string  {
		return "tbl_" + defaultTableName;
	}
	db, err := gorm.Open("mysql", "root:admin123@tcp(127.0.0.1:3306)/logger?charset=utf8mb4&parseTime=True&loc=Local")
	if err != nil {
		panic(err)
	}
	// 全局禁用表名复数
	db.SingularTable(true) // 如果设置为true,`User`的默认表名为`user`,不为复数
	DBInstance = db
	//defer closerDB()
}

//关闭数据库
func closerDB() {
	DBInstance.Close()
}

func CreateLogger(logger Logger, types string) error {
	return DBInstance.Model(&logger).Where("type = '"+types+"'").UpdateColumn("value", gorm.Expr("value + ?", 1)).Error
}

func main() {

	//获取参数
	logFilePath := flag.String("logFilePath", "/Users/syl/nginx_access.log", "日志文件路径")
	routineNum  := flag.Int("routineNum", 5, "消费并发数")
	l 			:= flag.String("log", "/Users/syl/log", "运行输出日志存储位置")
	// 把用户传递的命令行参数解析为对应变量的值
	flag.Parse()
	params 		:= cmdParams{*logFilePath,*routineNum}

	//日志打印
		logFd, err := os.OpenFile(*l, os.O_CREATE|os.O_WRONLY, 0644) //如果文件不存在创建，仅写模式打开
	if err == nil {
		log.Out = logFd
		defer  logFd.Close() //程序退出时关闭
	}
	log.Infof("Execute start.")
	log.Infof("Params: logFilePath=%s, routineNum =%d", params.logFilePath, params.routineNum)

	//初始化channel,进行数据传递
	var logChannel 		= make(chan string, 3*params.routineNum)
	var pvChannel  		= make(chan urlData, params.routineNum)
	var uvChannel  		= make(chan urlData, params.routineNum)
	var storageChannel 	= make(chan storageBlock, params.routineNum)

	//读取日志,并放到日志通道 (传递接收的参数 和 消费日志的通道)
	go readFileLog(params, logChannel)

	//日志处理，读取日志通道的数据，放入到pv和uv的通道（一组处理的routine）
	for i := 0; i < params.routineNum; i ++ {
		go consunmerLog(logChannel, pvChannel, uvChannel)
	}
	// Redis Pool
	redisPool, err := pool.New( "tcp", "10.98.41.99:6379", 2*params.routineNum );
	if err != nil{
		log.Fatalln( "Redis pool created failed." )
		panic(err)
	} else {
		//连接成功启动携程去判断，连接是否存活
		go func(){
			for{
				redisPool.Cmd( "PING" )
				time.Sleep( 3*time.Second )
			}
		}()
	}
	////PV UV 数据计算
	go pvCounter(pvChannel, storageChannel)
	go uvCounter(uvChannel, storageChannel, redisPool)

	////数据存储
	go dataStorage(storageChannel, redisPool)

	//挂起
	time.Sleep(1000*time.Second)
}

//读取日志
func readFileLog(params cmdParams, logChannel chan string) error {
	//打开文件
	fd, err := os.Open(params.logFilePath)
	if err != nil {
		log.Warningf("readFileLog 不能打开文件 file:%s", params.logFilePath)
	}

	defer fd.Close()

	count := 0
	bufferRead := bufio.NewReader(fd)

	for  {
		//读取
		line, err := bufferRead.ReadString('\n')

		if count%(1000*params.routineNum) == 0 {
			log.Infof("readFileLog line: %d", count)
		}
		if err != nil {
			if err == io.EOF {
				//读到行尾部，等待新日志写入
				time.Sleep(3*time.Second)
				log.Infof("readFileLog 等待新日志写入, read line: %d", count)
			} else {
				log.Warningf("readFileLog 读取失败")
			}
		} else {
			//放入通道
			logChannel <- line
			count++
		}

	}

	return nil
}

//解析日志
func cutLogFetchData(logStr string) logData {
	logStr	= strings.TrimSpace( logStr ) //去除空格
	pos1 	:= str.IndexOf(logStr, HANDLE_DIG, 0)
	if pos1 == -1 {
		return logData{} //没找到
	}
	pos1 	+= len(HANDLE_DIG)
	pos2 	:= str.IndexOf(logStr, "HTTP", pos1)
	d 		:= str.Substr(logStr, pos1, pos2-pos1) //截取到字符

	urlInfo,err := url.Parse("http://localhost/?"+d)
	if err != nil {

		return logData{}
	}
	data := urlInfo.Query()
	return logData{
		data.Get("time"),
		data.Get("refer"),
		data.Get("url"),
		data.Get("ua"),
	}
}

//日志处理消费
func consunmerLog(logChannel chan string, pvChannel, uvChannel chan urlData) error {

	for logStr := range logChannel {
		//if logStr == ""{
		//	return nil
		//}
		//切割日志，取出上报数据
		data := cutLogFetchData(logStr)
		hasher := md5.New()
		hasher.Write([]byte(data.refer+data.ua)) //模拟uid
		uid := hex.EncodeToString(hasher.Sum(nil))
		uData := urlData{ data, uid, formatUrl( data.url, data.time ) }

		log.Infoln(uData) //输出一下
		pvChannel <- uData
		uvChannel <- uData
	}
	return nil
}

//格式化数据
func formatUrl( url, t string ) urlNode{
	//详情页>列表页≥首页
	pos1 := str.IndexOf( url, HANDLE_MOVIE, 0)
	if pos1 != -1 {
		pos1 	+= len( HANDLE_MOVIE )
		pos2 	:= str.IndexOf( url, HANDLE_HTML, 0 )
		idStr 	:= str.Substr( url , pos1, pos2-pos1 )
		id, _ 	:= strconv.Atoi( idStr ) //转换整型
		return urlNode{ "movie", id, url, t }
	} else {
		pos1 = str.IndexOf( url, HANDLE_LIST, 0 )
		if pos1 != -1 {
			pos1	+= len( HANDLE_LIST )
			pos2 	:= str.IndexOf( url, HANDLE_HTML, 0 )
			idStr 	:= str.Substr( url , pos1, pos2-pos1 )
			id, _ 	:= strconv.Atoi( idStr )
			return urlNode{ "list", id, url, t }
		} else {
			//if url == "" {
			//	return urlNode{}
			//}else{
				return urlNode{ "home", 1, url, t}
			//}
		}
	}
}

//pv处理
func pvCounter(pvChannel chan urlData, storageChannel chan storageBlock) {
	//time.Sleep(3*time.Second)
	//l := len(pvChannel)
	//fmt.Println(l)
	for data := range pvChannel {
		sItem := storageBlock{"pv", "ZINCRBY", data.unode}
		storageChannel <- sItem
	}

}

//uv处理
func uvCounter(uvChannel chan  urlData, storageChannel chan storageBlock, redisPool *pool.Pool ) {
	//time.Sleep(3*time.Second)
	//l := len(uvChannel)
	//fmt.Println(l)
	for data := range uvChannel {
		//HyperLoglog
		hyperLogLogKey := "uv_hpll_" + getTime(data.data.time, "day")
		ret, err := redisPool.Cmd( "PFADD", hyperLogLogKey, data.uid, "EX", 86400 ).Int()
		if err != nil {
			log.Warningln( "UvCounter check redis hyperloglog failed", err )
		}
		if ret != 1 {
			continue
		}
		sItem := storageBlock{ "uv", "ZINCRBY", data.unode }
		storageChannel <- sItem
	}
}

//存储
func dataStorage(storageChannel chan storageBlock, redisPool *pool.Pool) {

	for block := range storageChannel {

		prefix := block.counterType + "_" //设置一个前缀

		setKeys := []string{
			prefix + "day_" + getTime(block.unode.unTime, "day"),
			//prefix + "hour_" + getTime(block.unode.unTime, "hour"),
			//prefix + "min_" + getTime(block.unode.unTime, "min"),
			//prefix + block.unode.unType + "_day_" + getTime(block.unode.unTime, "day"),
			//prefix + block.unode.unType + "_hour_" + getTime(block.unode.unTime, "hour"),
			//prefix + block.unode.unType + "_min_"  + getTime(block.unode.unTime, "min"),
		}
		rowId := block.unode.unRid

		for _,key := range setKeys {
			ret, err := redisPool.Cmd( block.storageModel, key, 10, rowId ).Int()
			if ret <= 0 || err != nil {
				log.Errorln( "DataStorage redis storage error.", block.storageModel, key, rowId )
			}
			l := Logger{block.counterType, rowId}
			res := CreateLogger(l, block.counterType);
			fmt.Println(res)
		}
	}
}


func getTime( logTime, timeType string ) string {
	var item string
	switch timeType {
	case "day":
		item = "2006-01-02"
		break
	case "hour":
		item = "2006-01-02 15"
		break
	case "min":
		item = "2006-01-02 15:04"
		break
	}
	t, _ := time.Parse( item, time.Now().Format(item) )
	return strconv.FormatInt( t.Unix(), 10 )
}