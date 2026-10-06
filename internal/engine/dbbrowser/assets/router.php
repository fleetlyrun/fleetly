<?php
// fleetly browse router for Adminer（ADR-0051 决策 3，vendored 胶水）。
// 载体命令：php -S 0.0.0.0:8080 -t /var/www/html /run/secrets/router.php
// 本文件经 Materials.SecretFiles 注入 /run/secrets/router.php（adminer:6.1.1
// 官方镜像不改上游代码；Apache-2.0/GPL-2.0 双许可生态中的自有胶水）。
//
// 机制（2026-10-07 本地 docker 实证，adminer:6.1.1）：
//   1. 零输入登录：GET / 时仿真一次登录 POST（携带秘密文件里的全凭证——
//      adminer 的会话密码取自 POST 的 set_password，credentials() 只管连接）；
//      verifyLoginToken() 返回 false 跳过登录 CSRF（6.1.0+ 官方外部认证桥，
//      面向"外部网站已认证"的场景）。
//   2. 会话 token 预铸：等价 stock 流程先 GET 登录页再 POST 的副作用——
//      缺席则第二跳判 "Session expired"（PoC 实录）。
//   3. 静态 adminer.css 交回 php -S（return false）；其余请求全部进 adminer。
//   4. 首入后的跳转只带 server/username/db 非密参数；密码只在 php 进程内存
//      （来自秘密文件）与 adminer 服务端会话——不进 env/argv/spec/URL。
//
// 本文件必须留在全局命名空间：adminer.php 以无限定名调用 adminer_object()，
// include 不继承别的文件的命名空间。

if (basename($_SERVER["REQUEST_URI"] ?? "/") === "adminer.css") {
    return false;
}

$conf = json_decode((string) file_get_contents("/run/secrets/db-conn.json"), true);
if (!is_array($conf) || empty($conf["host"]) || empty($conf["user"]) || !isset($conf["password"]) || !isset($conf["db"])) {
    http_response_code(503);
    exit("fleetly browse: connection material missing\n");
}

if (($_SERVER["REQUEST_METHOD"] ?? "") === "GET" && ($_SERVER["REQUEST_URI"] ?? "/") === "/") {
    session_name("adminer_sid");
    session_start();
    if (empty($_SESSION["token"])) {
        $_SESSION["token"] = rand(1, 1e6);
    }
    session_write_close();
    $_POST["auth"] = [
        "driver" => "server",
        "server" => $conf["host"],
        "username" => $conf["user"],
        "password" => $conf["password"],
        "db" => $conf["db"],
    ];
}

function adminer_object() {
    global $conf;
    return new Adminer\Plugins([new class($conf) extends Adminer\Plugin {
        public function __construct(private array $conf) {}
        public function credentials(...$args) {
            return [$this->conf["host"], $this->conf["user"], $this->conf["password"]];
        }
        public function database(...$args) {
            return $this->conf["db"];
        }
        public function verifyLoginToken(...$args) {
            return false; // 登录 POST 免 CSRF：平台 Launcher Ticket 门禁在前
        }
    }]);
}

require "/var/www/html/adminer.php";
