# Mẫu规划 đại cương

Tác dụng của mẫu này không phải ép tất cả tác phẩm thành độ dài cố định, mà giúp trước phán cấp độ tác phẩm, rồi chọn độ chi tiết đại cương.

## Bước 1: Phán cấp độ độ dài tác phẩm

### Ngắn / Đơn quyển

- Áp dụng: Đơn xung đột, đơn mục tiêu, nhân vật ít, kết cục tập trung
| Tham khảo độ dài: 8-25 chương
- Định dạng khuyến nghị: Phẳng `outline`

### Trung / Đa giai đoạn

- Áp dụng: Có thăng cấp giai đoạn, vài line phụ, quan hệ nhân vật biến đổi
| Tham khảo độ dài: 25-60 chương
- Định dạng khuyến nghị: Phẳng `outline` hoặc nhẹ phân tầng

### Dài kỳ连载 / Loại web novel

- Áp dụng: Thể loại thiên nhiên có không gian thăng cấp liên tục, sức căng quan hệ dài hạn, đa mục tiêu giai đoạn, thế giới可 mở rộng,谜 đoàn dài hạn hoặc line trưởng thành dài kỳ
| Tham khảo độ dài: 80-200+ chương
- Định dạng khuyến nghị: Phân tầng `layered_outline`

## Bước 2: Phán có bắt buộc dùng đại cương phân tầng không

Chỉ cần thỏa mãn bất kỳ 2 điều dưới, ưu tiên dùng `layered_outline`:

- Thế giới quan cần dần dần mở rộng, không phải lần một kể xong
| Trưởng thành nhân vật chính không phải một nhảy, mà đa giai đoạn thăng cấp
- Quan hệ nhân vật sẽ liên tục biến đổi ở đa giai đoạn
| Giữa và hậu kỳ tồn tại loại mâu thuẫn chính khác nhau
- Cần đa lần chuyển địa đồ/đổi thế lực/đổi thân phận/đổi mục tiêu
- Thể loại rõ ràng更像 loại tiểu thuyết thương mại连载, không phải đơn quyển

## Bước 3: Dài kỳ đừng trực tiếp làm "sổ流水 chương toàn sách"

Thứ tự规划 dài kỳ khuyến nghị:

1. Điểm bán tác phẩm và khác biệt hóa
2. Cỗ máy chuyện dài hạn
3. Chủ đề cấp quyển và thăng cấp
4. Mục tiêu cấp vòng và chuyển hướng giai đoạn
5. Sự kiện cấp chương và móc

Cách sai:

- Trước viết tóm tắt 20 chương, rồi cưỡng kéo dài
- Mỗi quyển lặp "gặp địch-mạnh lên-đổi địa đồ"
| Chỉ có line chính thăng cấp, không có line quan hệ thăng cấp
- Tiền kỳ透支 tất cả bí mật lớn, trung hậu kỳ chỉ lặp mẫu

## Mẫu đại cương phẳng (Ngắn/Trung)

```json
[
  {
    "chapter": 1,
    "title": "Tiêu đề chương",
    "core_event": "Sự kiện cốt lõi chương",
    "hook": "Móc cuối chương",
    "scenes": ["Cảnh 1", "Cảnh 2", "Cảnh 3"]
  }
]
```

## Mẫu đại cương phân tầng (Dài - Quyển vòng song tầng lăn mở)

规划 ban đầu dùng song tầng lăn: 2 quyển trước có vòng骨架, quyển còn lại là quyển骨架; vòng đầu có chương chi tiết.

```json
[
  {
    "index": 1,
    "title": "Tiêu đề quyển một",
    "theme": "Mâu thuẫn/chủ đề cốt lõi mới增 quyển này",
    "arcs": [
      {
        "index": 1,
        "title": "Vòng một (đã triển khai)",
        "goal": "Mục tiêu bộ phận, cản trở và chuyển hướng",
        "chapters": [
          {"chapter": 1, "title": "Tiêu đề chương", "core_event": "Sự kiện cốt lõi", "hook": "Móc cuối chương", "scenes": ["Cảnh 1", "Cảnh 2"]}
        ]
      },
      {
        "index": 2,
        "title": "Vòng hai (vòng骨架)",
        "goal": "Tóm tắt mục tiêu vòng này",
        "estimated_chapters": 12,
        "chapters": []
      }
    ]
  },
  {
    "index": 2,
    "title": "Tiêu đề quyển hai",
    "theme": "Chủ đề quyển hai",
    "arcs": [
      {"index": 1, "title": "Tiêu đề vòng", "goal": "Mục tiêu vòng", "estimated_chapters": 15, "chapters": []},
      {"index": 2, "title": "Tiêu đề vòng", "goal": "Mục tiêu vòng", "estimated_chapters": 10, "chapters": []}
    ]
  },
  {
    "index": 3,
    "title": "Tiêu đề quyển ba (quyển骨架)",
    "theme": "Hướng chủ đề quyển ba",
    "estimated_chapters": 60,
    "arcs": []
  }
]
```

- Triển khai cấp vòng: Viết đẩy đến vòng骨架时, Architect triển khai chương chi tiết vòng đó
- Triển khai cấp quyển: Viết đẩy đến quyển骨架时, Architect triển khai cấu trúc vòng + chương vòng đầu

## Danh sách kiểm cấp quyển dài kỳ

Mỗi quyển đều phải trả lời:

- Quyển này mới增 tin thế giới gì?
- Quyển này thăng cấp mâu thuẫn cốt lõi gì?
- Quyển này để nhân vật chính được gì, cũng mất gì?
- Quyển này thay đổi quan hệ nhân vật chính thế nào?
- Sau khi quyển này kết, chuyện vì sao bắt buộc vào quyển tiếp?

## Danh sách kiểm cấp vòng dài kỳ

Mỗi vòng đều phải trả lời:

- Mục tiêu rõ ràng của vòng này là gì?
- Cản trở đến từ ai, quy tắc nào, giá cả gì?
- Điểm chuyển hướng là gì?
- Sau khi vòng kết, trạng thái nào phát sinh biến đổi bất khả nghịch?

## Danh sách kiểm cấp chương

- Mỗi chương bắt buộc phục vụ mục tiêu vòng
- Mỗi chương bắt buộc chứa một sự kiện đẩy tiến không thể xóa
| Móc đa dạng, đừng toàn dựa vào mẫu "phát hiện bí mật"
- Chương tiền kỳ không thể chỉ "giới thiệu thế giới", bắt buộc đồng bộ đẩy nhân vật và xung đột
