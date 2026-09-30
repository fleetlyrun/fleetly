# 时间语义

一条裁决定死三处：Schedule 存带时区 cron（IANA 时区名随 Schedule 持久化，跨夏令时由 cron 库按墙钟解释）；TTL 与 Owner Lease 落库为**绝对 deadline**（控制面重启后按墙钟续算，不依赖进程内计时器）；审计与 Event 时间戳一律 UTC RFC3339。
