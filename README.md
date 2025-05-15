Иногда у вас есть файл, куда приходит множество сообщений от разных устройств.
Хотелось бы увидеть эти сообщения в Zabbix, прямо у нужного устройства.
Собственно, этот скриптик парсит лог-файл и шлёт сообщения куда нужно.
К устройству нужно прицепить шаблон (или создать итем ручками, но мы ведь говорим о сотнях устройств...)

rsyslog настраивается примерно так вот:

```
$template netFormat,"%timegenerated:::date-rfc3339% %fromhost-ip% %syslogtag%%msg:::sp-if-no-1st-sp%%msg:::drop-last-lf%\n"
$ModLoad imudp
$UDPServerAddress 10.77.0.1
$UDPServerRun 514

:inputname, isequal, "imudp" /var/log/net.log;netFormat
& stop
```
