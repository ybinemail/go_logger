curl --location-trusted -u root:unisound@123 -T ./sql.csv -H "label:csv_user_info" -H "column_separator:," http://192.168.3.247:8030/api/starrocks/user_info/_stream_load