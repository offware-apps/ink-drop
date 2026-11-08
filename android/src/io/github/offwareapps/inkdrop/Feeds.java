package io.github.offwareapps.inkdrop;

import android.util.Xml;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.URI;
import java.util.ArrayList;
import java.util.List;
import org.xmlpull.v1.XmlPullParser;
import org.xmlpull.v1.XmlPullParserException;
import org.xmlpull.v1.XmlSerializer;

// Feeds is the feed list, kept as one OPML file, the format every feed reader
// imports and exports. The file is parsed, never run.
final class Feeds {
  static final class Feed {
    final String title;
    final String url;
    final String topic;
    final String about;
    // What the feed keeps, set on its own screen: each book keepDays days, 0
    // for ever, and its newest keepMost, 0 for any number. Every feed keeps
    // all its books until the user picks a limit there.
    int keepDays, keepMost;
    // Words, comma-separated: an article holding a mute word is left out, and
    // with only set, one holding none of its words.
    String mute = "", only = "";
    // One book per refresh, holding every new article, rather than one each.
    boolean together;

    Feed(String title, String url, String topic, String about) {
      this.title = title.isEmpty() ? host(url) : title;
      this.url = url;
      this.topic = topic;
      this.about = about;
    }
  }

  private final File file;
  final List<Feed> list = new ArrayList<>();

  Feeds(File file) {
    this.file = file;
    if (!file.exists()) return;
    try (InputStream in = new FileInputStream(file)) {
      for (Feed f : read(in, "")) add(f);
    } catch (IOException | XmlPullParserException e) {
      // A damaged list starts empty rather than stopping the app, and is kept
      // beside it, since the next save would write over it.
      file.renameTo(new File(file.getPath() + ".bad"));
      list.clear();
    }
  }

  boolean has(String url) {
    return get(url) != null;
  }

  // get returns the feed at url, or null when the list holds none.
  Feed get(String url) {
    for (Feed f : list) if (f.url.equals(url)) return f;
    return null;
  }

  // add keeps one entry per address and reports whether it was new.
  boolean add(Feed f) {
    if (has(f.url)) return false;
    list.add(f);
    return true;
  }

  void remove(String url) {
    list.removeIf(f -> f.url.equals(url));
  }

  void save() throws IOException {
    File tmp = new File(file.getPath() + ".tmp");
    try (OutputStream out = new FileOutputStream(tmp)) {
      write(out);
    }
    if (!tmp.renameTo(file)) throw new IOException("could not replace " + file);
  }

  void write(OutputStream out) throws IOException {
    XmlSerializer x = Xml.newSerializer();
    x.setOutput(out, "UTF-8");
    x.startDocument("UTF-8", null);
    x.startTag(null, "opml").attribute(null, "version", "2.0");
    x.startTag(null, "head").startTag(null, "title").text("ink-drop feeds").endTag(null, "title").endTag(null, "head");
    x.startTag(null, "body");
    for (Feed f : list) {
      x.startTag(null, "outline").attribute(null, "type", "rss").attribute(null, "text", f.title)
          .attribute(null, "title", f.title).attribute(null, "xmlUrl", f.url);
      if (!f.topic.isEmpty()) x.attribute(null, "category", f.topic);
      if (!f.about.isEmpty()) x.attribute(null, "description", f.about);
      // ink-drop's own settings; other readers skip attributes they do not know.
      if (f.keepDays != 0) x.attribute(null, "inkKeepDays", Integer.toString(f.keepDays));
      if (f.keepMost != 0) x.attribute(null, "inkKeepMost", Integer.toString(f.keepMost));
      if (!f.mute.isEmpty()) x.attribute(null, "inkMute", f.mute);
      if (!f.only.isEmpty()) x.attribute(null, "inkOnly", f.only);
      if (f.together) x.attribute(null, "inkTogether", "true");
      x.endTag(null, "outline");
    }
    x.endTag(null, "body").endTag(null, "opml");
    x.endDocument();
    out.flush();
  }

  // read returns every outline carrying an http or https feed address; an
  // outline holding others names their topic, else topic is used.
  static List<Feed> read(InputStream in, String topic) throws IOException, XmlPullParserException {
    XmlPullParser p = Xml.newPullParser();
    // Lists in the wild carry a bare & in their addresses; the relaxed parser
    // reads it as text rather than stopping at the first one.
    p.setFeature("http://xmlpull.org/v1/doc/features.html#relaxed", true);
    p.setInput(in, null);
    List<Feed> out = new ArrayList<>();
    List<String> topics = new ArrayList<>();
    for (int e = p.getEventType(); e != XmlPullParser.END_DOCUMENT; e = p.next()) {
      if (e == XmlPullParser.END_TAG && p.getName().equals("outline")) {
        if (!topics.isEmpty()) topics.remove(topics.size() - 1);
        continue;
      }
      if (e != XmlPullParser.START_TAG || !p.getName().equals("outline")) continue;
      String name = first(p.getAttributeValue(null, "title"), p.getAttributeValue(null, "text"));
      String url = first(p.getAttributeValue(null, "xmlUrl"), "");
      topics.add(url.isEmpty() ? name : "");
      if (url.startsWith("https://") || url.startsWith("http://")) {
        String t = topic;
        for (String s : topics) if (!s.isEmpty()) t = s;
        Feed f = new Feed(name, url, first(p.getAttributeValue(null, "category"), t), first(p.getAttributeValue(null, "description"), ""));
        f.keepDays = count(p.getAttributeValue(null, "inkKeepDays"), 0);
        f.keepMost = count(p.getAttributeValue(null, "inkKeepMost"), 0);
        f.mute = first(p.getAttributeValue(null, "inkMute"), "");
        f.only = first(p.getAttributeValue(null, "inkOnly"), "");
        f.together = "true".equals(p.getAttributeValue(null, "inkTogether"));
        out.add(f);
      }
    }
    return out;
  }

  static String host(String url) {
    try {
      String h = new URI(url).getHost();
      return h == null ? url : h.replaceFirst("^www\\.", "");
    } catch (Exception e) {
      return url;
    }
  }

  // count returns the whole number s holds, or otherwise when it holds none.
  private static int count(String s, int otherwise) {
    try {
      return s == null ? otherwise : Math.max(0, Integer.parseInt(s.trim()));
    } catch (NumberFormatException e) {
      return otherwise;
    }
  }

  private static String first(String a, String b) {
    return a != null && !a.trim().isEmpty() ? a.trim() : b == null ? "" : b;
  }
}
