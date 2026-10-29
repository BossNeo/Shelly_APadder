# Локална Shelly станция: инсталация

**Статус: подготвен код, без инсталация или изпитване на физически рутер.** Потребителят потвърди Chateau LTE7 ax. Точният board-name, версията и интерфейсите все още трябва да бъдат прочетени от устройството преди промени.

## Проверка

Използвай `deploy/inspect-router.rsc` в RouterOS Terminal. Той само чете и отпечатва избрани настройки. Преди инсталиране запази и свали export и криптирано binary backup; не споделяй show-sensitive export.

Нужни са точният `board-name`, версия и архитектура, съответстващи `wifi-qcom` и `container` пакети, физическите 2.4/5 GHz радиа, трите съществуващи виртуални мрежи, LAN bridge и свободно място. Панелът не работи с CAPsMAN радио, радио с директен IP или със съществуващ DHCP client. Не предполага, че `wifi1` е 2.4 GHz.

## Памет и образ

Chateau LTE7 ax има 1 GB RAM и 128 MB NAND. `/partitions` разделя паметта за отделни RouterOS инсталации; `repartition` преформатира NAND и рестартира. За панела не е необходим. Използвай директория/том за приложението, за предпочитане на подготвен USB носител. Няма включени команди за форматиране. Вътрешна инсталация изисква измерване на извлечения образ и свободното място с резерв за обновяване на RouterOS.

На компютър с Docker и buildx:

```sh
docker buildx build --platform linux/arm64 --load -t shelly-panel:local .
docker save --output shelly-panel-arm64.tar shelly-panel:local
```

Първото построяване изтегля официалния Python base image. Прехвърли готовия tar локално; рутерът не тегли образа и приложението няма външни зависимости. В сесията няма Docker/Podman, затова образът не е построен или проверен.

## RouterOS и мрежа

Инсталирай официалния container пакет за точната RouterOS версия/архитектура. `/system/device-mode/update container=yes` изисква физическото потвърждение, показано от рутера, и може да го рестартира. Не сменяй целия device-mode и не включвай partitions за приложението.

Прегледай `deploy/routeros-network.rsc.example` **по секции** срещу реалния firewall, FastTrack, IPv6 и адресен план. Шаблонът предполага LAN `192.168.88.0/24`, свободен сегмент `172.31.253.0/29` и съществуващи filter правила. Не го импортирай сляпо; `place-before=0` трябва да се адаптира при празен firewall. Контейнерът има достъп само до RouterOS REST; Shelly RPC се изпълнява чрез RouterOS fetch. Панелът е на `192.168.88.1:8088`, WebFig запазва порта си. Не се добавя интернет NAT или WAN публикуване.

Създай отделен RouterOS потребител с `read,write,test,sensitive,rest-api` и провери точните необходими политики на инсталираната версия. Групите са широки права, а не ACL по меню; ограничи REST до адреса на контейнера. Запази съществуващия административен достъп. За HTTP REST е нужен активен `www` във вътрешния сегмент. За HTTPS използвай `www-ssl` и доверен CA; проверката на сертификати не се изключва.

Задай локално в `/container/envs`, списък `shelly-panel`:

| Ключ | Стойност |
| --- | --- |
| `ROUTER_URL` | `http://172.31.253.1` за отделния вътрешен сегмент |
| `ROUTER_USER` | отделният RouterOS потребител |
| `ROUTER_PASSWORD` | неговата парола |
| `PANEL_PASSWORD` | отделна парола за панела, минимум 12 знака |

Не записвай реални пароли в Git, снимки или чат. При HTTPS REST монтирай CA и задай `ROUTER_CA_FILE`. За HTTPS на панела са налични `PANEL_TLS_CERT` и `PANEL_TLS_KEY`; сертификатът трябва да е доверен от браузъра. За HTTP панел използвай само доверена WPA2/WPA3 мрежа за управление или кабел.

## Данни и контейнер

Копирай `deploy/config.example.json` като `config.json` в постоянната директория за `/data`. Попълни точния модел, имената на радиата/bridge, трите разрешени виртуални интерфейса в `allowed_existing` и адресите на панела в `allowed_hosts`. За първия старт остави `allow_changes=false`.

Следните команди са само пример за подготвен носител `usb1`, качен tar и готови директории. Провери имената и mount синтаксиса на конкретната RouterOS версия:

```routeros
/container/mounts/add list=shelly-panel-data src=usb1/shelly-panel/data dst=/data
/container/add file=usb1/shelly-panel-arm64.tar name=shelly-panel interface=sp-veth root-dir=usb1/shelly-panel/root mountlists=shelly-panel-data envlist=shelly-panel start-on-boot=yes logging=yes
/container/print
```

Изчакай извличането да приключи и стартирай точния върнат container ID. Отвори `http://192.168.88.1:8088`. Провери модела, радиата и трите работни мрежи. `/data/state.json` съдържа SSID пароли и recovery журнал; пази носителя и архивите като пароли. Файлът не се сервира през HTTP; новите state файлове са с права 0600 в Linux. Директорията трябва да е writable за container процеса.

## Проверки на реалното устройство

1. Потвърди независим достъп през 5 GHz/кабел и backup извън рутера.
2. Провери нулирането и възстановяването на директните Wi-Fi настройки и native execute job на конкретния firmware.
3. Задай `allow_changes=true`, рестартирай контейнера и изпитай един изключен SSID.
4. Сканирай веднъж; провери точните предишни SSID състояния, bridge порта и радио настройки след връщане.
5. Добави едно тестово Shelly с оставен AP. Провери DHCP, идентичност и целевия SSID. След това изпитай изключване на AP.
6. На тестова сесия спри контейнера и провери native watchdog след три минути и startup recovery от журнала.
7. Изключи WAN и повтори основните операции и нормална работа като рутер.

Не променяй Wi-Fi едновременно през WebFig по време на операция: възстановява се снимката от началото. След грешка recovery журналът блокира нови промени и панелът предлага „Възстанови рутера“. При router reboot native jobs не се пазят и е нужен startup recovery.

## Обхват

Shelly Gen2+, фабричен отворен AP без Shelly парола, DHCP. Облачните настройки се запазват. Новите SSID са в съществуващия LAN — не са изолирани VLAN/подмрежи. Статичните настройки от стария скрипт остават само в `TheScript`; за постоянни адреси използвай DHCP reservations след добавяне. Съществуващите три мрежи се добавят като профили с настоящите SSID/пароли и не се преконфигурират.

Автоматичните тестове симулират RouterOS. Няма визуална проверка през браузъра след спирането на Computer Use; няма доказана съвместимост с физическия рутер.

Източници: [хардуер](https://mikrotik.com/product/chateau_lte7_ax), [Containers](https://help.mikrotik.com/docs/spaces/ROS/pages/84901929/Container), [Partitions](https://help.mikrotik.com/docs/spaces/ROS/pages/328103/Partitions), [REST](https://manual.mikrotik.com/docs/developer-guides/rest-api/), [Wi-Fi](https://manual.mikrotik.com/docs/wireless/wifi/), [Shelly WiFi RPC](https://shelly-api-docs.shelly.cloud/gen2/ComponentsAndServices/WiFi/).

