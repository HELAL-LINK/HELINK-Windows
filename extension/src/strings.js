'use strict';
/* The words of the HELINK extension, in Modern Standard Arabic and in English. The
   language follows the browser unless chosen in the settings. */

var HELINK_STRINGS = {
  ar: {
    menuLink: 'تنزيل الرابط بـ HELINK',
    menuMedia: 'تنزيل هذا الفيديو بـ HELINK',
    menuPage: 'تنزيل فيديو هذه الصفحة بـ HELINK',
    menuAudio: 'تنزيل هذا الصوت بـ HELINK',

    statusRunning: 'HELINK يعمل',
    statusClosed: 'HELINK مغلق الآن، وسيبدأ وحده عند الحاجة',
    statusMissing: 'لم يُعثر على برنامج HELINK',
    statusMissingHelp: 'ثبّت HELINK على الكمبيوتر وافتحه مرة واحدة، ثم أعد فتح هذه النافذة.',
    statusForbidden: 'برنامج HELINK لا يعرف هذه الإضافة بعد',
    statusForbiddenHelp: 'افتح برنامج HELINK مرة واحدة ليتعرّف عليها، ثم أعد فتح هذه النافذة.',
    statusError: 'تعذّر الاتصال ببرنامج HELINK',
    getApp: 'تنزيل HELINK',
    catchLabel: 'التقاط التنزيلات من المتصفح',
    catchHint: 'تنزيل أسرع بعدة اتصالات، مع استئناف بعد الانقطاع',
    pageVideo: 'تنزيل فيديو هذه الصفحة',
    foundTitle: 'وجدته الإضافة في هذه الصفحة',
    noneFound: 'لم يظهر فيديو بعد. شغّل الفيديو، ثم افتح هذه النافذة مرة أخرى.',
    kindVideo: 'فيديو',
    kindAudio: 'صوت',
    kindStream: 'بث',
    download: 'تنزيل',
    openApp: 'فتح HELINK',
    settings: 'الإعدادات',
    sent: 'أُرسل إلى HELINK',
    failed: 'تعذّر الإرسال إلى HELINK',
    notWeb: 'لا يمكن التنزيل من هذه الصفحة.',

    optTitle: 'إعدادات HELINK',
    optLang: 'اللغة',
    langAuto: 'لغة المتصفح',
    secConnection: 'الاتصال ببرنامج HELINK',
    testConn: 'اختبار الاتصال',
    testing: 'جارٍ الاختبار…',
    hostOk: 'مساعد HELINK للمتصفح مثبّت.',
    hostMissing: 'مساعد HELINK للمتصفح غير مثبّت: ثبّت HELINK وافتحه مرة واحدة.',
    appRunning: 'برنامج HELINK يعمل (الإصدار {v}).',
    appClosed: 'برنامج HELINK مغلق، وسيبدأ عند أول تنزيل.',
    secDownloads: 'التنزيلات',
    catchOpt: 'يلتقط HELINK التنزيلات من المتصفح',
    dialogOpt: 'عرض نافذة «تنزيل جديد» قبل البدء',
    typesLabel: 'أنواع الملفات التي يلتقطها',
    typesHint: 'امتدادات تفصل بينها مسافات، مثل: zip exe mp4',
    typesReset: 'استعادة القائمة الأصلية',
    minSize: 'أصغر حجم يلتقطه (كيلوبايت)',
    minSizeHint: 'الملفات الأصغر يُنزّلها المتصفح بنفسه. 0 لالتقاط كل الأحجام.',
    skipSites: 'مواقع يُنزّل منها المتصفح بنفسه',
    sitesHint: 'موقع في كل سطر، مثل: example.com',
    secButtons: 'زر التنزيل على الفيديوهات',
    buttonsOpt: 'إظهار زر HELINK فوق الفيديوهات في الصفحات',
    buttonsHint: 'يظهر الزر عند تمرير الفأرة فوق الفيديو أو عند تشغيله، ويمكنك سحبه إلى أي مكان.',
    buttonSkip: 'مواقع بلا زر',
    resetPos: 'إعادة الأزرار إلى أماكنها',
    resetDone: 'عادت الأزرار إلى أماكنها.',
    save: 'حفظ',
    saved: 'حُفظت الإعدادات.',
    savedLocal: 'حُفظت في الإضافة، وستصل إلى HELINK عند تشغيله.',
    secAbout: 'عن الإضافة',
    aboutText: 'إضافة HELINK تنقل التنزيلات والفيديوهات من المتصفح إلى برنامج HELINK على الكمبيوتر.',
    version: 'الإصدار',

    btnDownload: 'تنزيل',
    btnHide: 'إخفاء',
    btnSending: 'جارٍ الإرسال…',
    btnSent: 'أُرسل إلى HELINK',
    btnNoApp: 'ثبّت برنامج HELINK أولًا',
    btnFailed: 'تعذّر الإرسال'
  },
  en: {
    menuLink: 'Download link with HELINK',
    menuMedia: 'Download this video with HELINK',
    menuPage: "Download this page's video with HELINK",
    menuAudio: 'Download this audio with HELINK',

    statusRunning: 'HELINK is running',
    statusClosed: 'HELINK is closed now; it starts by itself when needed',
    statusMissing: "The HELINK app wasn't found",
    statusMissingHelp: 'Install HELINK on this computer and open it once, then open this window again.',
    statusForbidden: "The HELINK app doesn't know this extension yet",
    statusForbiddenHelp: 'Open the HELINK app once so it can recognise it, then open this window again.',
    statusError: "Couldn't reach the HELINK app",
    getApp: 'Get HELINK',
    catchLabel: 'Take downloads from the browser',
    catchHint: 'Faster downloads over several connections, resumed after interruptions',
    pageVideo: "Download this page's video",
    foundTitle: 'Found on this page',
    noneFound: 'No video yet. Play the video, then open this window again.',
    kindVideo: 'Video',
    kindAudio: 'Audio',
    kindStream: 'Stream',
    download: 'Download',
    openApp: 'Open HELINK',
    settings: 'Settings',
    sent: 'Sent to HELINK',
    failed: "Couldn't send it to HELINK",
    notWeb: "Downloads can't start from this page.",

    optTitle: 'HELINK settings',
    optLang: 'Language',
    langAuto: "Browser's language",
    secConnection: 'Connection to the HELINK app',
    testConn: 'Test the connection',
    testing: 'Testing…',
    hostOk: "HELINK's browser helper is installed.",
    hostMissing: "HELINK's browser helper isn't installed: install HELINK and open it once.",
    appRunning: 'The HELINK app is running (version {v}).',
    appClosed: 'The HELINK app is closed; it starts with the first download.',
    secDownloads: 'Downloads',
    catchOpt: 'HELINK takes downloads from the browser',
    dialogOpt: 'Show the "New download" window before starting',
    typesLabel: 'Kinds of files it takes',
    typesHint: 'Extensions separated by spaces, like: zip exe mp4',
    typesReset: 'Restore the original list',
    minSize: 'Smallest size it takes (KB)',
    minSizeHint: 'Smaller files are downloaded by the browser. 0 takes every size.',
    skipSites: 'Sites the browser downloads from itself',
    sitesHint: 'One site per line, like: example.com',
    secButtons: 'Download button on videos',
    buttonsOpt: 'Show the HELINK button over videos in web pages',
    buttonsHint: 'The button appears when the mouse is over a video or when it plays, and you can drag it anywhere.',
    buttonSkip: 'Sites without the button',
    resetPos: 'Put the buttons back in place',
    resetDone: 'The buttons are back in place.',
    save: 'Save',
    saved: 'Settings saved.',
    savedLocal: 'Saved in the extension; HELINK gets them when it runs.',
    secAbout: 'About',
    aboutText: 'The HELINK extension hands downloads and videos from the browser to the HELINK app on this computer.',
    version: 'Version',

    btnDownload: 'Download',
    btnHide: 'Hide',
    btnSending: 'Sending…',
    btnSent: 'Sent to HELINK',
    btnNoApp: 'Install the HELINK app first',
    btnFailed: "Couldn't send it"
  }
};

/* The language to use: "ar" or "en". */
function helinkLang(pref) {
  if (pref === 'ar' || pref === 'en') return pref;
  var ui = '';
  try {
    ui = (globalThis.browser || globalThis.chrome).i18n.getUILanguage() || '';
  } catch (e) {
    ui = (globalThis.navigator && navigator.language) || '';
  }
  return /^ar\b/i.test(ui) ? 'ar' : 'en';
}

function helinkT(lang, key, vars) {
  var t = (HELINK_STRINGS[lang] || HELINK_STRINGS.en)[key];
  if (t === undefined) t = HELINK_STRINGS.en[key] || key;
  if (vars) {
    Object.keys(vars).forEach(function (k) { t = t.split('{' + k + '}').join(vars[k]); });
  }
  return t;
}

/* IDM's list of files taken from the browser, as HELINK has it. */
var HELINK_DEFAULT_TYPES = ['3gp', '7z', 'aac', 'ace', 'aif', 'apk', 'arj', 'asf', 'avi', 'bin', 'bz2', 'dmg', 'exe',
  'flac', 'flv', 'gz', 'gzip', 'img', 'iso', 'lzh', 'm4a', 'm4v', 'mkv', 'mov', 'mp3', 'mp4', 'mpeg', 'mpg', 'msi',
  'msu', 'ogg', 'ogv', 'opus', 'pdf', 'qt', 'rar', 'rm', 'rmvb', 'sit', 'sitx', 'tar', 'tgz', 'torrent', 'ts', 'wav',
  'webm', 'wma', 'wmv', 'xz', 'z', 'zip', 'zst', 'docx', 'xlsx', 'pptx', 'doc', 'xls', 'ppt', 'epub', 'deb', 'rpm',
  'appx', 'msix', 'cab', 'jar', 'vob', 'mts', 'm2ts'];
